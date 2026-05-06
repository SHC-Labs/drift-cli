package clients

import (
	"os"
	"path/filepath"
)

// ClaudeCodeInfo describes whether Claude Code is detected on this machine
// and where its config lives.
type ClaudeCodeInfo struct {
	Installed  bool   // true if ~/.claude/ exists
	HomeDir    string // ~/.claude (the dir we'd write hook configs into)
	SettingsGS string // ~/.claude/settings.json (global settings, drift install does not touch this)
}

// DetectClaudeCode returns whether Claude Code is installed on this machine.
// The heuristic: presence of ~/.claude/. Cheap, reliable, and the same
// signal the bash install template used.
//
// Per-project hook registration lives in drift init, which writes
// <project>/.claude/settings.local.json. drift install does NOT touch
// the global settings.json; that's a deliberate choice carried over
// from the bash install.
//
// Kept around for callers that want the typed ClaudeCodeInfo struct;
// new code should use DetectAll() in detect.go for multi-client work.
func DetectClaudeCode() ClaudeCodeInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return ClaudeCodeInfo{}
	}
	dir := filepath.Join(home, ".claude")
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return ClaudeCodeInfo{}
	}
	return ClaudeCodeInfo{
		Installed:  true,
		HomeDir:    dir,
		SettingsGS: filepath.Join(dir, "settings.json"),
	}
}
