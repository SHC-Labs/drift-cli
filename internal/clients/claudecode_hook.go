package clients

import (
	"bytes"
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

	// Normalize the binary path for shell consumption. On Windows
	// os.Executable() returns backslash paths like
	// `C:\Users\tony\.local\bin\drift.exe`. Claude Code wraps hook
	// commands in bash on Windows by default; bash strips backslashes
	// as escape chars during -c parsing, so the binary fails to launch
	// silently with no stdout. The brain-context hooks Tony has
	// working all use forward slashes, which both bash on Windows
	// (Git Bash/MSYS) and cmd.exe accept. Normalizing fixes the path
	// without forcing customers to add a `shell` field.
	hookExe := normalizeExePathForHook(exePath)

	driftCheck := hookEntry{
		Tag: driftHookMarker,
		Hooks: []hookCommand{{
			Type:    "command",
			Command: shellQuote(hookExe) + " internal hook prompt-submit",
			Timeout: HookCommandTimeout,
		}},
	}
	driftReport := hookEntry{
		Matcher: PostToolUseMatcher,
		Tag:     driftHookMarker,
		Hooks: []hookCommand{{
			Type:    "command",
			Command: shellQuote(hookExe) + " internal hook post-tool-use",
			Timeout: PostToolUseTimeout,
		}},
	}

	// Strip any orphan drift commands a customer (or a prior install)
	// may have manually merged into a non-drift entry. Without this
	// the upsert below appends a fresh tagged entry alongside the
	// orphan, doubling the firing rate.
	hooks["UserPromptSubmit"] = scrubDriftCommandsFromMixedEntries(hooks["UserPromptSubmit"])
	hooks["PostToolUse"] = scrubDriftCommandsFromMixedEntries(hooks["PostToolUse"])

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
		// First sweep: drop tagged entries + pure-drift untagged entries.
		filtered := make([]hookEntry, 0, len(entries))
		for _, e := range entries {
			if e.Tag == driftHookMarker {
				continue
			}
			if isLegacyDriftHookEntry(e) {
				continue
			}
			filtered = append(filtered, e)
		}
		// Second sweep: scrub orphan drift commands from any mixed
		// user entries that survived the first pass.
		filtered = scrubDriftCommandsFromMixedEntries(filtered)
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
// CRITICAL: matches only when EVERY hook command in the entry is a
// drift command. An entry that mixes drift commands with user-owned
// commands (e.g., a customer manually merged drift into their existing
// rag-auto-search entry) is NOT legacy — replacing it would wipe the
// user's work. scrubDriftCommandsFromMixedEntries handles the mixed
// case by surgically removing only the drift commands.
//
// Used by upsertHookEntry so the Go binary's installer can replace
// pure-drift untagged entries in place rather than appending new
// tagged entries alongside the broken legacy ones, and by
// unregisterHooksAt so uninstall removes them too.
func isLegacyDriftHookEntry(e hookEntry) bool {
	if e.Tag != "" {
		// Anything tagged isn't legacy. drift-managed handled by the
		// caller; foreign tags are user-owned.
		return false
	}
	if len(e.Hooks) == 0 {
		return false
	}
	for _, h := range e.Hooks {
		if h.Type != "command" {
			return false
		}
		if !isDriftHookCommand(h.Command) {
			return false
		}
	}
	return true
}

// isDriftHookCommand recognizes any command string that invokes a
// drift hook handler — current Go binary subcommands or the legacy
// bash-CLI wrappers. Used by isLegacyDriftHookEntry (entire-entry
// match) and scrubDriftCommandsFromMixedEntries (per-hook removal).
func isDriftHookCommand(cmd string) bool {
	c := strings.ToLower(cmd)
	if strings.Contains(c, "internal hook prompt-submit") ||
		strings.Contains(c, "internal hook post-tool-use") {
		return true
	}
	if strings.Contains(c, "drift-check.bat") ||
		strings.Contains(c, "drift-check.sh") ||
		strings.Contains(c, "drift-check.mjs") ||
		strings.Contains(c, "drift-report.bat") ||
		strings.Contains(c, "drift-report.sh") ||
		strings.Contains(c, "drift-report.mjs") {
		return true
	}
	return false
}

// scrubDriftCommandsFromMixedEntries removes drift hook commands from
// untagged entries that ALSO contain user-owned commands. Customers
// (or earlier versions of this installer + manual merge scripts) may
// have appended a drift command into a pre-existing entry alongside
// their own hooks. The strict isLegacyDriftHookEntry would skip those
// entries (correctly preserving the user's hooks), but that leaves
// the orphan drift command in place and it would fire twice once we
// upsert a fresh tagged entry. Sweep them out first.
//
// Drift-tagged entries are left alone (upsertHookEntry replaces them
// wholesale). Pure-drift untagged entries are also left alone here
// (upsertHookEntry replaces them via the legacy path). Only mixed
// entries get surgical edits.
func scrubDriftCommandsFromMixedEntries(entries []hookEntry) []hookEntry {
	out := make([]hookEntry, 0, len(entries))
	for _, e := range entries {
		if e.Tag == driftHookMarker || isLegacyDriftHookEntry(e) {
			out = append(out, e)
			continue
		}
		// Untagged + non-pure-drift = either purely user-owned (no
		// changes needed) or a mixed entry with a drift command we
		// need to strip. Filter by command predicate.
		cleaned := make([]hookCommand, 0, len(e.Hooks))
		removed := false
		for _, h := range e.Hooks {
			if h.Type == "command" && isDriftHookCommand(h.Command) {
				removed = true
				continue
			}
			cleaned = append(cleaned, h)
		}
		if !removed {
			// Pure user entry, no changes needed.
			out = append(out, e)
			continue
		}
		if len(cleaned) == 0 {
			// All hooks in the entry were drift commands; the strict
			// isLegacyDriftHookEntry should have caught this above,
			// but defensively drop the now-empty entry.
			continue
		}
		e.Hooks = cleaned
		out = append(out, e)
	}
	return out
}

// utf8BOM is the three-byte UTF-8 byte-order mark some Windows text
// editors (Notepad, PowerShell 5.1's Set-Content -Encoding UTF8, the
// VS Code "save with BOM" option) prepend to UTF-8 files. Go's
// encoding/json refuses to parse a BOM-prefixed input with `invalid
// character 'ï' looking for beginning of value`, which silently
// breaks drift install on machines where settings.json has been
// touched by a BOM-emitting tool. v0.1.15 surfaced this on Tony's
// Magnum PC: install reported "register Claude Code hooks globally:
// parse C:\\Users\\...\\settings.json: invalid character 'ï' ..." and
// the hook never got registered.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// readRawSettings loads .claude/settings.local.json into a generic map.
// Missing file returns an empty map so callers can build from scratch.
// BOM-prefixed UTF-8 files (the PowerShell Set-Content -Encoding UTF8
// default on Windows PowerShell 5.1) are tolerated by stripping the
// BOM before parsing.
func readRawSettings(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
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

// normalizeExePathForHook converts a Windows backslash path (the form
// os.Executable() returns) into a forward-slash path that bash on
// Windows can resolve. Claude Code defaults to invoking hook commands
// via bash even on Windows; bash -c parsing treats backslashes as
// escape characters and strips them, so a command like
// `C:\Users\tony\.local\bin\drift.exe` becomes `C:Userstony.localbindrift.exe`
// after escape stripping and the binary fails to launch silently with
// no stdout (the v0.1.10-v0.1.14 Magnum failure mode).
//
// Forward slashes work in cmd, PowerShell, and bash on Windows because
// the Win32 API accepts both. The brain-context hooks Tony has
// working all use forward-slash paths for the same reason.
//
// On non-Windows platforms this is a no-op.
func normalizeExePathForHook(p string) string {
	return strings.ReplaceAll(p, `\`, `/`)
}
