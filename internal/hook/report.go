package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/SHC-Labs/drift/internal/config"
)

// httpTimeoutReport is the per-request budget for the report hook's POST.
// Mirrors the bash `curl -m 2`. Shorter than the check-updates timeout
// because PostToolUse fires after every tool use and we don't want to
// stall the agent on slow networks.
const httpTimeoutReport = 2 * time.Second

// reportToolInput is the subset of Claude Code's PostToolUse stdin payload
// that drift-report needs. Other fields are ignored.
type reportToolInput struct {
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// PostToolUse is the entry point the cobra subcommand calls. Reads stdin
// JSON, looks up the file's project, fires off /api/report-edit best-effort.
// Always returns nil and exits 0; PostToolUse must NEVER block the agent
// on a network call.
//
// No loud-failure mode (parity with the bash hook): silent skip on every
// gate. PostToolUse fires after every tool use and noisy output would spam
// the activity feed.
func PostToolUse(ctx context.Context, stdin io.Reader) error {
	body, err := io.ReadAll(stdin)
	if err != nil || len(body) == 0 {
		return nil
	}
	var input reportToolInput
	if err := json.Unmarshal(body, &input); err != nil {
		return nil
	}
	filePath := input.ToolInput.FilePath
	if filePath == "" {
		return nil
	}

	mcp, err := config.ReadMCP()
	if err != nil {
		return nil
	}

	fileDir := filepath.Dir(filePath)
	repoRoot := gitTopLevel(fileDir)
	repoURL := gitRemoteOrigin(fileDir)

	searchDir := repoRoot
	if searchDir == "" {
		searchDir = fileDir
	}
	driftPath, err := config.WalkUpForDrift(searchDir)
	if errors.Is(err, config.ErrDriftConfigNotFound) {
		return nil
	}
	if err != nil {
		return nil
	}

	cfg, err := config.ReadDrift(driftPath)
	if err != nil || !cfg.ShouldReport() {
		return nil
	}

	files := NormalizeFilePath(filePath, repoRoot)

	// project_hash input mirrors the bash hook: git remote URL when
	// available, dirname(.drift.json) as the fallback for non-git
	// projects.
	hashInput := repoURL
	if hashInput == "" {
		hashInput = filepath.Dir(driftPath)
	}
	projectHash := Sha256Hex(hashInput)

	payload := map[string]any{
		"description":  "Editing",
		"files":        files,
		"repo_url":     repoURL,
		"project_hash": projectHash,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil
	}

	// Fire detached: spawn a goroutine that lives only as long as the
	// HTTP call, return immediately. The bash hook used `curl ... &` and
	// `exit 0` to the same effect.
	go func() {
		fireCtx, cancel := context.WithTimeout(context.Background(), httpTimeoutReport)
		defer cancel()
		req, err := http.NewRequestWithContext(fireCtx, http.MethodPost,
			mcp.BaseURL+"/api/report-edit", bytes.NewReader(bodyBytes))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+mcp.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
	// Wait briefly so the goroutine has time to actually issue the
	// request before main() returns. 100ms is plenty for a localhost
	// proxy hop and well under the 2s HTTP budget. Without this, a
	// fast-exiting main() can kill the goroutine before the syscall
	// reaches the kernel. The bash hook avoided this by spawning curl
	// as a separate process via `&`; in Go we must keep the parent
	// alive long enough.
	time.Sleep(100 * time.Millisecond)
	return nil
}

