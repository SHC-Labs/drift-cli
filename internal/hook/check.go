package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SHC-Labs/drift/internal/config"
)

// httpTimeoutCheck is the per-request budget for the prompt-submit hook's
// HTTP calls. Mirrors the bash `curl -m 3`. Total wall-clock is still
// dominated by these two calls (policy sync + check-updates).
const httpTimeoutCheck = 3 * time.Second

// PromptSubmit is the entry point the cobra subcommand calls. Reads env +
// ~/.mcp.json + .drift.json, syncs project policy if changed, fetches team
// activity, emits a <drift-context> block to stdout. Always returns nil;
// loud-failure mode emits its own context block instead of a Go error.
//
// Caller (cobra cmd RunE) returns nil unconditionally so the AI client
// gets exit 0 even on misconfiguration -- the loud-context block is the
// signal, not the exit code.
func PromptSubmit(ctx context.Context, stdout io.Writer) error {
	mcp, err := config.ReadMCP()
	if err != nil {
		EmitInactive(stdout, mcpInactiveReason(err))
		return nil
	}

	// Dual walk-up: CLAUDE_PROJECT_DIR first, then PWD if they differ.
	// Catches the Claude Code case where the IDE workspace folder is a
	// parent above the actual project root.
	projectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	if projectDir == "" {
		projectDir, _ = os.Getwd()
	}
	driftPath, err := config.WalkUpForDrift(projectDir)
	if errors.Is(err, config.ErrDriftConfigNotFound) {
		pwd, _ := os.Getwd()
		if pwd != "" && pwd != projectDir {
			driftPath, err = config.WalkUpForDrift(pwd)
		}
	}
	if errors.Is(err, config.ErrDriftConfigNotFound) {
		pwd, _ := os.Getwd()
		EmitInactive(stdout, fmt.Sprintf(
			"no .drift.json found by walking up from %s (CLAUDE_PROJECT_DIR) or %s. Run 'drift init' inside the project root to opt in.",
			projectDir, pwd))
		return nil
	}
	if err != nil {
		EmitInactive(stdout, fmt.Sprintf("walk-up for .drift.json failed: %v", err))
		return nil
	}

	cfg, err := config.ReadDrift(driftPath)
	if err != nil {
		EmitInactive(stdout, fmt.Sprintf("could not parse %s: %v", driftPath, err))
		return nil
	}
	if !cfg.Enabled {
		EmitInactive(stdout, fmt.Sprintf(
			".drift.json found at %s but enabled is not true. Set \"enabled\": true in that file (or re-run 'drift init') to flip it on.",
			driftPath))
		return nil
	}

	projectDirForHash := filepath.Dir(driftPath)
	projectHash := ProjectHash(projectDirForHash)

	// Best-effort policy sync: only PUT when content changed since last sync.
	syncProjectPolicy(ctx, mcp, cfg, projectHash)

	// Fetch team activity. State-hash diff lets server short-circuit when
	// nothing changed.
	cacheDir, _ := CacheDir()
	hashFile := filepath.Join(cacheDir, "state-hash")
	cachedHash := readCachedHash(hashFile)

	body, httpCode, fetchErr := fetchCheckUpdates(ctx, mcp, cachedHash)
	if fetchErr != nil && httpCode == 0 {
		EmitInactive(stdout, fmt.Sprintf(
			"could not reach the local drift relay at %s. Is the drift service running? Check with 'drift status'; restart with 'drift install' if the port shows 'not set'.",
			mcp.BaseURL))
		return nil
	}
	if httpCode == http.StatusUnauthorized || httpCode == http.StatusForbidden {
		EmitInactive(stdout, fmt.Sprintf(
			"Drift server rejected the API token (HTTP %d). The token in ~/.mcp.json is invalid or revoked. Rotate the key from app.driftlabs.io/profile and re-run install with DRIFT_TOKEN=<new-key>.",
			httpCode))
		return nil
	}
	if httpCode != http.StatusOK && httpCode != http.StatusNoContent {
		head := body
		if len(head) > 200 {
			head = head[:200]
		}
		EmitInactive(stdout, fmt.Sprintf(
			"Drift server returned HTTP %d on /api/check-updates. Body head: %s",
			httpCode, head))
		return nil
	}
	if body == "" {
		return nil
	}

	// First line "DRIFT_HASH:..." is the new state hash; cache it for next
	// call and strip from content.
	content := body
	if newline := strings.IndexByte(body, '\n'); newline >= 0 {
		first := body[:newline]
		if strings.HasPrefix(first, "DRIFT_HASH:") {
			newHash := strings.TrimPrefix(first, "DRIFT_HASH:")
			writeCachedHash(hashFile, newHash)
			content = body[newline+1:]
		}
	} else if strings.HasPrefix(body, "DRIFT_HASH:") {
		writeCachedHash(hashFile, strings.TrimPrefix(body, "DRIFT_HASH:"))
		content = ""
	}

	if content == "" {
		return nil
	}

	emitContextBlock(stdout, content, projectHash, cfg, driftPath)
	return nil
}

// mcpInactiveReason maps an MCP-config error to the loud-failure message
// the bash hook prints. Each branch matches a specific bash gate.
func mcpInactiveReason(err error) string {
	switch {
	case errors.Is(err, config.ErrMCPMissing):
		return "~/.mcp.json missing. Reinstall via 'curl -fsSL https://mcp.driftlabs.io/install | sh'."
	case errors.Is(err, config.ErrMCPCorrupt):
		return "~/.mcp.json is corrupt. Re-run 'drift install' to back up the bad file and rebuild fresh."
	case errors.Is(err, config.ErrDriftServerMissing):
		return "no Drift entry in ~/.mcp.json mcpServers. Reinstall via 'curl -fsSL https://mcp.driftlabs.io/install | sh'."
	case errors.Is(err, config.ErrTokenMissing):
		return "no Drift token in ~/.mcp.json. Re-run the install one-liner with DRIFT_TOKEN=<your-key> from app.driftlabs.io/profile."
	case errors.Is(err, config.ErrTokenPlaceholder):
		return "~/.mcp.json still contains the YOUR_DRIFT_TOKEN placeholder. Re-run install with DRIFT_TOKEN=<your-key> from app.driftlabs.io/profile, or edit ~/.mcp.json by hand and replace the placeholder."
	case errors.Is(err, config.ErrURLMissing):
		return "no Drift URL in ~/.mcp.json. Reinstall."
	default:
		return fmt.Sprintf("could not read ~/.mcp.json: %v", err)
	}
}

// readCachedHash returns the contents of the state-hash cache file, or
// empty string on any error.
func readCachedHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeCachedHash writes the new hash to disk best-effort. Failures are
// silent (the next call will just re-fetch the full state).
func writeCachedHash(path, hash string) {
	_ = os.WriteFile(path, []byte(hash+"\n"), 0o644)
}

// fetchCheckUpdates calls GET /api/check-updates?state_hash=<cached> with a
// 3s budget. Returns the raw body, HTTP code, and any transport error.
// Body is returned even on non-2xx so the loud-context can include the
// head of it.
func fetchCheckUpdates(ctx context.Context, mcp *config.MCPConfig, cachedHash string) (string, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeoutCheck)
	defer cancel()

	url := mcp.BaseURL + "/api/check-updates"
	if cachedHash != "" {
		url += "?state_hash=" + cachedHash
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+mcp.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode, nil
}

// syncProjectPolicy mirrors the bash policy-sync gate: only PUT when the
// .drift.json content hash differs from the cached "last synced" value.
// Best-effort: any failure leaves the cache untouched and the next call
// will retry. Never blocks the hook on this.
func syncProjectPolicy(ctx context.Context, mcp *config.MCPConfig, cfg *config.DriftConfig, projectHash string) {
	if projectHash == "" {
		return
	}
	contentBytes, err := os.ReadFile(cfg.Path)
	if err != nil {
		return
	}
	contentSum := sha256.Sum256(contentBytes)
	contentHash := hex.EncodeToString(contentSum[:])

	cachePath := filepath.Join(os.TempDir(), "drift-policy-synced-"+projectHash)
	last, _ := os.ReadFile(cachePath)
	if strings.TrimSpace(string(last)) == contentHash {
		return
	}

	body, err := buildPolicyBody(projectHash, contentHash, cfg, contentBytes)
	if err != nil {
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, httpTimeoutCheck)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut,
		mcp.BaseURL+"/api/projects/policy", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+mcp.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = os.WriteFile(cachePath, []byte(contentHash), 0o644)
	}
}

// buildPolicyBody mirrors drift-helpers.mjs `build-policy-body`. The
// server's /api/projects/policy expects {project_hash, content_hash, policy}
// where policy is the parsed .drift.json content.
func buildPolicyBody(projectHash, contentHash string, cfg *config.DriftConfig, contentBytes []byte) ([]byte, error) {
	var policy json.RawMessage = contentBytes
	body := map[string]any{
		"project_hash": projectHash,
		"content_hash": contentHash,
		"policy":       policy,
	}
	return json.Marshal(body)
}

// emitContextBlock writes the success-with-content output. Mirrors the
// bash hook's tail section: server content, project identifier, optional
// policy block, REQUIRED steps, optional task handling.
func emitContextBlock(w io.Writer, content, projectHash string, cfg *config.DriftConfig, driftPath string) {
	hasConflicts := strings.Contains(content, "Active team work")
	hasTasks := strings.Contains(content, "task(s) assigned")

	fmt.Fprintln(w, "<drift-context>")
	// Trim trailing newlines on content to keep the spacing tidy. The bash
	// echo'd content + blank line; we do the same. Sanitize the server
	// payload first: an upstream that returned a literal </drift-context>
	// could otherwise close this block early and inject text the LLM
	// reads as a system instruction.
	fmt.Fprintln(w, SanitizeForContextBlock(strings.TrimRight(content, "\n")))
	fmt.Fprintln(w, "")

	if projectHash != "" {
		fmt.Fprintln(w, "PROJECT IDENTIFIER -- when calling any drift_* tool from this project, include:")
		fmt.Fprintf(w, "  project_hash: %q\n", projectHash)
		fmt.Fprintln(w, "The Drift server enforces this project's .drift.json policy using that hash.")
		fmt.Fprintln(w, "")
	}

	if len(cfg.DeniedTools) > 0 {
		// Sanitize both the path and the tool names. .drift.json is a
		// repo-checked-in file: a malicious commit could plant marker
		// strings in denied_tools entries (or, in pathological cases,
		// repo names) and escape the context block. Same defense we
		// apply to server-supplied content.
		sanitizedTools := make([]string, len(cfg.DeniedTools))
		for i, tool := range cfg.DeniedTools {
			sanitizedTools[i] = SanitizeForContextBlock(tool)
		}
		fmt.Fprintf(w, "PROJECT POLICY -- %s marks this project with restrictions.\n", SanitizeForContextBlock(driftPath))
		fmt.Fprintf(w, "Tools that MUST NOT be called from this project: %s\n", strings.Join(sanitizedTools, ", "))
		fmt.Fprintln(w, "Refuse the call if the user asks. Tell them this project's .drift.json denies it.")
		fmt.Fprintln(w, "")
	}

	fmt.Fprintln(w, "REQUIRED -- Do ALL of these steps in order:")
	fmt.Fprintln(w, "1. Tell the user the Drift team status at the START of your response. Use ✅ when no conflicts, ⚠️ when there are conflicts.")
	fmt.Fprintln(w, "2. BEFORE your first file edit, call drift_declare_intent with the files you plan to modify and a brief description. TELL THE USER you are declaring your intent.")
	if hasConflicts {
		fmt.Fprintln(w, "3. ⚠️ CONFLICT: If YOUR planned edits touch ANY of the same files another developer is working on, STOP and ask the user: '[Developer] is editing [file] -- should I proceed, wait, or work on a different file?' Do NOT edit conflicting files without explicit user approval.")
	} else {
		fmt.Fprintln(w, "3. ✅ No conflicts -- proceed with edits after declaring intent.")
	}
	if hasTasks {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "TASK HANDLING (when tasks are assigned):")
		fmt.Fprintln(w, "a. If you were given a task, ASK the user first -- do NOT auto-claim. Say: 'You have a task assigned: [title]. Want me to start on it?'")
		fmt.Fprintln(w, "b. If user agrees, call drift_claim_task with the task_id.")
		fmt.Fprintln(w, "c. Follow the STOP-before-edit procedure on every file change (check_conflicts -> declare_intent -> edit -> broadcast_change).")
		fmt.Fprintln(w, "d. When the work is complete, call drift_complete_task with status='completed' and a result summary listing what changed.")
		fmt.Fprintln(w, "e. Do NOT git commit or git push. The task creator reviews your diff and ships it. Leaving files uncommitted is correct behavior.")
	}
	fmt.Fprintln(w, "</drift-context>")
}
