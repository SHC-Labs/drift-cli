package clients

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SHC-Labs/drift/internal/config"
)

// ProjectSetupResult describes what drift init did for one client in
// the current project. Each client gets its own entry in the install
// summary so the customer sees what changed where.
type ProjectSetupResult struct {
	ID         ClientID
	ConfigPath string
	HookPath   string // hook config path if hook registration applies (Claude Code)
	HintPath   string // .cursorrules / AGENTS.md / etc path written for non-hooks-aware clients
	Action     string // "wrote", "updated", "skipped:not-detected"
	Err        error
}

// SetupProject configures every detected MCP client for the current
// project. relayURL is the local relay URL; exePath is the absolute
// path to the drift binary (used in hook commands).
//
// For each detected client:
//   - Write the per-project MCP config file (Cursor's .cursor/mcp.json,
//     VS Code's .vscode/mcp.json, etc) pointing at the local relay.
//   - For hooks-aware clients (Claude Code only in v1), register the
//     two hook entries.
//   - For non-hooks-aware clients (Cursor, Windsurf, etc), write a
//     hint file (.cursorrules / AGENTS.md / similar) telling the agent
//     to call drift_* tools manually. Detect existing hint files and
//     append non-destructively.
func SetupProject(projectDir, relayURL, exePath string) ([]ProjectSetupResult, error) {
	detected := DetectAll()
	var results []ProjectSetupResult
	for _, d := range detected {
		r := setupOne(d, projectDir, relayURL, exePath)
		results = append(results, r)
	}
	return results, nil
}

func setupOne(d Detected, projectDir, relayURL, exePath string) ProjectSetupResult {
	r := ProjectSetupResult{ID: d.ID}
	switch d.ID {
	case ClaudeCode:
		path, err := RegisterClaudeCodeHooks(projectDir, exePath)
		r.HookPath = path
		r.Err = err
		if err == nil {
			r.Action = "wrote"
		}
	case Cursor:
		mcpPath := filepath.Join(projectDir, ".cursor", "mcp.json")
		if _, err := writeStandardMCP(mcpPath, relayURL); err != nil {
			r.Err = err
			return r
		}
		r.ConfigPath = mcpPath
		hintPath, err := appendCursorRules(projectDir)
		if err != nil {
			r.Err = err
			return r
		}
		r.HintPath = hintPath
		r.Action = "wrote"
	case VSCode:
		mcpPath := filepath.Join(projectDir, ".vscode", "mcp.json")
		if _, err := writeStandardMCP(mcpPath, relayURL); err != nil {
			r.Err = err
			return r
		}
		r.ConfigPath = mcpPath
		r.Action = "wrote"
	case Windsurf, Antigravity, Kilo, Kimi, Zed:
		// These clients use AGENTS.md as the per-project agent-rules
		// file. We append a drift section to AGENTS.md (or create it
		// if missing) describing the drift_* tools the agent should
		// call manually.
		hintPath, err := appendAgentsMD(projectDir, string(d.ID))
		if err != nil {
			r.Err = err
			return r
		}
		r.HintPath = hintPath
		r.Action = "wrote"
	default:
		r.Action = "skipped:no-handler"
	}
	return r
}

// driftSectionMarker bookends the drift section of any hint file we
// touch. RegisterClaudeCodeHooks uses a JSON _drift_tag for the same
// purpose; for plain-text files we use a marker comment.
const driftSectionMarker = "<!-- drift-managed -->"

const cursorRulesContent = `## drift coordination (drift-managed)

This project is wired to Drift for team coordination. Before any file edit:

1. Call drift_check_conflicts to see what teammates are working on.
2. Call drift_declare_intent with the files you plan to modify and a brief description.
3. After committing, call drift_broadcast_change with the files + description.

If a teammate is editing the same files, STOP and ask the user before proceeding.

Available drift tools (call them via the drift MCP server in this project's mcp.json):
  drift_check_conflicts, drift_declare_intent, drift_broadcast_change,
  drift_get_team_activity, drift_send_message, drift_query_knowledge,
  drift_remember, drift_recall, drift_observe.
`

const agentsMDContent = `## drift coordination (drift-managed)

This project uses Drift for team coordination across humans and AI agents.

Before file edits:
1. Call drift_check_conflicts to surface teammate intents.
2. Call drift_declare_intent with files + description.
3. After commit, call drift_broadcast_change.

If a teammate is editing the same files, stop and ask the user.

Drift tools (via the drift MCP server):
  drift_check_conflicts, drift_declare_intent, drift_broadcast_change,
  drift_get_team_activity, drift_send_message, drift_query_knowledge,
  drift_remember, drift_recall, drift_observe.
`

// appendCursorRules adds a drift-managed section to .cursorrules in
// projectDir. Idempotent: re-running replaces the previous drift
// section, leaving every other line untouched. If .cursorrules
// doesn't exist, creates it.
func appendCursorRules(projectDir string) (string, error) {
	path := filepath.Join(projectDir, ".cursorrules")
	return upsertHintSection(path, cursorRulesContent)
}

// appendAgentsMD adds a drift-managed section to AGENTS.md in
// projectDir. Same upsert semantics as appendCursorRules.
func appendAgentsMD(projectDir, clientID string) (string, error) {
	path := filepath.Join(projectDir, "AGENTS.md")
	return upsertHintSection(path, agentsMDContent)
}

// upsertHintSection rewrites the drift-tagged section of path. The
// section is bracketed by driftSectionMarker so we can find it and
// replace without touching other content.
func upsertHintSection(path, body string) (string, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return path, fmt.Errorf("read %s: %w", path, err)
	}
	content := string(existing)
	startMarker := driftSectionMarker
	endMarker := "<!-- /drift-managed -->"

	startIdx := strings.Index(content, startMarker)
	endIdx := strings.Index(content, endMarker)
	newSection := startMarker + "\n" + body + endMarker + "\n"

	var out string
	switch {
	case startIdx >= 0 && endIdx > startIdx:
		// Replace existing section.
		out = content[:startIdx] + newSection + content[endIdx+len(endMarker)+1:]
	case content == "":
		// Empty file or missing: write just the section.
		out = newSection
	default:
		// File exists but no drift section yet: append with separator.
		sep := "\n\n"
		if strings.HasSuffix(content, "\n") {
			sep = "\n"
		}
		out = content + sep + newSection
	}
	return path, config.AtomicWriteFile(path, []byte(out), 0o644)
}

// RemoveProjectSetup reverses SetupProject for the current project.
// Removes the per-project MCP configs (Cursor, VS Code), unregisters
// the Claude Code hooks, and removes the drift-managed sections from
// hint files. Idempotent.
func RemoveProjectSetup(projectDir string) []ProjectSetupResult {
	detected := DetectAll()
	var results []ProjectSetupResult
	for _, d := range detected {
		r := teardownOne(d, projectDir)
		results = append(results, r)
	}
	return results
}

func teardownOne(d Detected, projectDir string) ProjectSetupResult {
	r := ProjectSetupResult{ID: d.ID}
	switch d.ID {
	case ClaudeCode:
		path, err := UnregisterClaudeCodeHooks(projectDir)
		r.HookPath = path
		r.Err = err
		r.Action = "removed"
	case Cursor:
		path := filepath.Join(projectDir, ".cursor", "mcp.json")
		if err := removeDriftFromMCP(path); err != nil {
			r.Err = err
		}
		r.ConfigPath = path
		hintPath := filepath.Join(projectDir, ".cursorrules")
		_ = removeHintSection(hintPath)
		r.HintPath = hintPath
		r.Action = "removed"
	case VSCode:
		path := filepath.Join(projectDir, ".vscode", "mcp.json")
		if err := removeDriftFromMCP(path); err != nil {
			r.Err = err
		}
		r.ConfigPath = path
		r.Action = "removed"
	case Windsurf, Antigravity, Kilo, Kimi, Zed:
		hintPath := filepath.Join(projectDir, "AGENTS.md")
		_ = removeHintSection(hintPath)
		r.HintPath = hintPath
		r.Action = "removed"
	default:
		r.Action = "skipped:no-handler"
	}
	return r
}

// removeDriftFromMCP strips just the "drift" entry from a per-project
// mcp.json. Other entries are preserved. Missing file is not an error.
func removeDriftFromMCP(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return nil
	}
	delete(servers, "drift")
	root["mcpServers"] = servers
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return config.AtomicWriteFile(path, out, 0o600)
}

// removeHintSection deletes the drift-managed section from a hint
// file (.cursorrules / AGENTS.md). Leaves other content alone.
// Missing file is not an error.
func removeHintSection(path string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	content := string(existing)
	startMarker := driftSectionMarker
	endMarker := "<!-- /drift-managed -->"
	startIdx := strings.Index(content, startMarker)
	endIdx := strings.Index(content, endMarker)
	if startIdx < 0 || endIdx <= startIdx {
		return nil
	}
	out := content[:startIdx] + content[endIdx+len(endMarker)+1:]
	out = strings.TrimRight(out, "\n") + "\n"
	if strings.TrimSpace(out) == "" {
		return os.Remove(path)
	}
	return config.AtomicWriteFile(path, []byte(out), 0o644)
}
