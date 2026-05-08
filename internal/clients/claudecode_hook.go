package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SHC-Labs/drift/internal/config"
)

// HookCommandTimeout is the per-hook timeout Claude Code respects when
// spawning the hook command. 5s is the existing bash hook's budget; we
// keep it for parity.
const HookCommandTimeout = 5

// PostToolUseTimeout is shorter because PostToolUse fires after every
// tool use and we don't want to stall the agent.
const PostToolUseTimeout = 3

// PostToolUseMatcher is the regex Claude Code matches against tool
// names to decide which tools fire the PostToolUse hook. Edit + Write
// are the file-modifying tools the report hook cares about.
const PostToolUseMatcher = "Edit|Write"

// driftHookMarker identifies hook entries this binary owns. Used to
// idempotently upsert without duplicating entries on re-install and to
// safely remove on uninstall without touching unrelated hooks.
const driftHookMarker = "drift-managed"

// claudeSettings is the on-disk shape of <project>/.claude/settings.local.json.
// Other top-level keys round-trip via Other so we don't drop user
// settings we don't know about.
type claudeSettings struct {
	Hooks map[string][]hookEntry     `json:"hooks,omitempty"`
	Other map[string]json.RawMessage `json:"-"`
}

// hookEntry is one matcher + N hook commands. Multiple entries per
// event can stack; we use Tag to identify the drift entry on re-write.
type hookEntry struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
	// Tag is non-standard but persisted so we can find our entry on
	// upsert / uninstall without grepping command strings.
	Tag string `json:"_drift_tag,omitempty"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// RegisterClaudeCodeHooks writes UserPromptSubmit + PostToolUse hook
// entries to <projectDir>/.claude/settings.local.json that point at the
// drift binary at exePath. Idempotent: re-calling upserts the entries
// keyed by driftHookMarker, leaving every other hook + every other top-
// level setting untouched.
//
// As of v0.1.13 the install command also writes the same entries to
// the global ~/.claude/settings.json via RegisterClaudeCodeHooksGlobal.
// The project-level call here is retained for backward compatibility +
// for clients that prefer per-project hook configs, but the global
// path is the primary one because Claude Code drops project-level
// hooks when the global file already defines a handler for the same
// event (the symptom that bit the v0.1.12 Magnum test).
//
// Mirrors the bash drift-helpers.mjs registerHooks behavior except we
// invoke "drift internal hook ..." subcommands directly instead of
// shelling to a separate .sh file.
func RegisterClaudeCodeHooks(projectDir, exePath string) (string, error) {
	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
	return registerHooksAt(settingsPath, exePath)
}

// RegisterClaudeCodeHooksGlobal writes the drift hook entries to the
// global ~/.claude/settings.json so they fire for every Claude Code
// session on this machine, regardless of the project. Same upsert
// semantics as the per-project version: existing drift entries get
// replaced, every other hook is preserved.
//
// Why global: Claude Code's hook cascade silently drops project-level
// entries when global already defines a handler for the same event
// (the v0.1.12 Magnum failure mode). Global is the only registration
// point that always fires. The hook itself walks up from cwd to find
// .drift.json so non-drift projects emit an INACTIVE response and
// don't pollute unrelated work.
func RegisterClaudeCodeHooksGlobal(exePath string) (string, error) {
	return registerHooksAt(globalClaudeSettingsPath(), exePath)
}

// registerHooksAt is the shared implementation: read the settings file,
// upsert the drift entries under UserPromptSubmit + PostToolUse, write
// back atomically. settingsPath determines whether this is the global
// or a per-project registration; behavior is otherwise identical.
func registerHooksAt(settingsPath, exePath string) (string, error) {
	root, err := readRawSettings(settingsPath)
	if err != nil {
		return "", err
	}

	hooks, err := decodeHooksField(root)
	if err != nil {
		return "", err
	}

	driftCheck := hookEntry{
		Tag: driftHookMarker,
		Hooks: []hookCommand{{
			Type:    "command",
			Command: shellQuote(exePath) + " internal hook prompt-submit",
			Timeout: HookCommandTimeout,
		}},
	}
	driftReport := hookEntry{
		Matcher: PostToolUseMatcher,
		Tag:     driftHookMarker,
		Hooks: []hookCommand{{
			Type:    "command",
			Command: shellQuote(exePath) + " internal hook post-tool-use",
			Timeout: PostToolUseTimeout,
		}},
	}

	hooks["UserPromptSubmit"] = upsertHookEntry(hooks["UserPromptSubmit"], driftCheck)
	hooks["PostToolUse"] = upsertHookEntry(hooks["PostToolUse"], driftReport)

	return settingsPath, writeHooksField(settingsPath, root, hooks)
}

// globalClaudeSettingsPath returns ~/.claude/settings.json, the global
// hook config Claude Code reads for every session. We use settings.json
// (committed-style) rather than settings.local.json because the
// committed file is the durable source of truth; Claude Code merges
// both at the same precedence level so either works, but settings.json
// is what users expect machine-level config to live in.
func globalClaudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fallback to the relative path; the install caller will
		// surface the failure when readRawSettings tries to open it.
		return filepath.Join(".claude", "settings.json")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// UnregisterClaudeCodeHooks removes drift-tagged entries from
// <projectDir>/.claude/settings.local.json. Other hooks (any entry
// without our tag) are preserved. Idempotent: removing on a settings
// file that has no drift entries is a no-op.
func UnregisterClaudeCodeHooks(projectDir string) (string, error) {
	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
	return unregisterHooksAt(settingsPath)
}

// UnregisterClaudeCodeHooksGlobal removes drift-tagged entries from
// the global ~/.claude/settings.json. Pair to RegisterClaudeCodeHooksGlobal.
// Called by drift uninstall.
func UnregisterClaudeCodeHooksGlobal() (string, error) {
	return unregisterHooksAt(globalClaudeSettingsPath())
}

// unregisterHooksAt is the shared implementation for both project and
// global unregister. Removes any entry tagged drift-managed AND any
// untagged entry whose command points at a drift binary's `internal
// hook` subcommand (covers v0.1.0-v0.1.12 entries that predate tagging
// or got duplicated during partial upgrades).
func unregisterHooksAt(settingsPath string) (string, error) {
	root, err := readRawSettings(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return settingsPath, nil
		}
		return settingsPath, err
	}
	hooks, err := decodeHooksField(root)
	if err != nil {
		return settingsPath, err
	}
	for event, entries := range hooks {
		filtered := entries[:0]
		for _, e := range entries {
			if e.Tag == driftHookMarker {
				continue
			}
			if isLegacyDriftHookEntry(e) {
				continue
			}
			filtered = append(filtered, e)
		}
		if len(filtered) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = filtered
		}
	}
	return settingsPath, writeHooksField(settingsPath, root, hooks)
}

// upsertHookEntry replaces any existing drift-tagged entry in the slice
// with newEntry. If no drift-tagged entry exists but a legacy bash-CLI
// entry (untagged, command pointing at one of the old wrapper scripts)
// is present, replaces THAT instead — covers the upgrade-from-bash-CLI
// path where customers have stale .bat/.sh hooks Claude Code can't
// even spawn anymore. If neither exists, appends. Preserves every
// other entry (matcher position + ordering) so unrelated hooks keep
// firing as the user configured them.
func upsertHookEntry(entries []hookEntry, newEntry hookEntry) []hookEntry {
	for i, e := range entries {
		if e.Tag == driftHookMarker {
			entries[i] = newEntry
			return entries
		}
	}
	for i, e := range entries {
		if isLegacyDriftHookEntry(e) {
			entries[i] = newEntry
			return entries
		}
	}
	return append(entries, newEntry)
}

// isLegacyDriftHookEntry returns true when entry looks like one a
// previous drift install owned but that didn't carry the drift-managed
// tag we now use to identify our entries. Two flavors covered:
//
//  1. Pre-Go-binary bash CLI: command points at a drift-check or
//     drift-report wrapper script (.bat on Windows, .sh on Unix,
//     .mjs in some early variants).
//  2. Pre-v0.1.13 Go binary: command runs the binary directly via
//     `<path>/drift[.exe] internal hook ...`. v0.1.0-v0.1.12 wrote
//     entries without the _drift_tag field on at least some install
//     paths (the upgrade flow re-inserted alongside the prior
//     untagged entry, leaving Tony with two duplicate entries on
//     Magnum). v0.1.13 sweeps these so re-install converges on a
//     single tagged entry.
//
// Used by upsertHookEntry so the Go binary's installer can replace
// these in place rather than appending new tagged entries alongside
// the broken legacy ones, and by unregisterHooksAt so uninstall
// removes them too.
func isLegacyDriftHookEntry(e hookEntry) bool {
	if e.Tag != "" {
		// Anything tagged isn't legacy. drift-managed handled by the
		// caller; foreign tags are user-owned.
		return false
	}
	for _, h := range e.Hooks {
		if h.Type != "command" {
			continue
		}
		cmd := strings.ToLower(h.Command)
		if strings.Contains(cmd, "drift-check.bat") ||
			strings.Contains(cmd, "drift-check.sh") ||
			strings.Contains(cmd, "drift-check.mjs") ||
			strings.Contains(cmd, "drift-report.bat") ||
			strings.Contains(cmd, "drift-report.sh") ||
			strings.Contains(cmd, "drift-report.mjs") {
			return true
		}
		// v0.1.0-v0.1.12 untagged binary entries. The "internal hook"
		// subcommand is the unambiguous fingerprint: end users would
		// never wire that string into a Claude Code hook by hand, and
		// the cobra command is hidden so external scripts can't depend
		// on it. Match on substring to tolerate any path/quoting style.
		if strings.Contains(cmd, "internal hook prompt-submit") ||
			strings.Contains(cmd, "internal hook post-tool-use") {
			return true
		}
	}
	return false
}

// readRawSettings loads .claude/settings.local.json into a generic map.
// Missing file returns an empty map so callers can build from scratch.
func readRawSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]json.RawMessage
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	return root, nil
}

// decodeHooksField pulls the "hooks" field out of root into a typed
// map. Missing field returns an empty map so callers can populate.
func decodeHooksField(root map[string]json.RawMessage) (map[string][]hookEntry, error) {
	hooks := map[string][]hookEntry{}
	raw, ok := root["hooks"]
	if !ok {
		return hooks, nil
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, fmt.Errorf("parse hooks field: %w", err)
	}
	return hooks, nil
}

// writeHooksField re-encodes the hooks field back into root and writes
// the whole file atomically. If hooks is empty, removes the field
// entirely so we don't leave an orphan "hooks": {} after uninstall.
func writeHooksField(path string, root map[string]json.RawMessage, hooks map[string][]hookEntry) error {
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		raw, err := json.Marshal(hooks)
		if err != nil {
			return fmt.Errorf("marshal hooks: %w", err)
		}
		root["hooks"] = raw
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return config.AtomicWriteFile(path, out, 0o644)
}

// shellQuote wraps a path in double-quotes if it contains spaces or
// other shell-meaningful characters. Claude Code's hook runner spawns
// commands via the OS shell on most platforms; quoting paths with
// spaces (common on Windows: "C:\\Program Files\\drift\\drift.exe") is
// required to prevent the shell from splitting on the space.
func shellQuote(p string) string {
	if !strings.ContainsAny(p, " \t\"") {
		return p
	}
	// Escape internal double quotes; surround with double quotes.
	return `"` + strings.ReplaceAll(p, `"`, `\"`) + `"`
}
