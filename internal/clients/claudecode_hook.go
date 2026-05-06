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
// Mirrors the bash drift-helpers.mjs registerHooks behavior except we
// invoke "drift internal hook ..." subcommands directly instead of
// shelling to a separate .sh file.
func RegisterClaudeCodeHooks(projectDir, exePath string) (string, error) {
	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
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

// UnregisterClaudeCodeHooks removes drift-tagged entries from
// <projectDir>/.claude/settings.local.json. Other hooks (any entry
// without our tag) are preserved. Idempotent: removing on a settings
// file that has no drift entries is a no-op.
func UnregisterClaudeCodeHooks(projectDir string) (string, error) {
	settingsPath := filepath.Join(projectDir, ".claude", "settings.local.json")
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
			if e.Tag != driftHookMarker {
				filtered = append(filtered, e)
			}
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
// with newEntry. If no drift-tagged entry exists, appends. Preserves
// every other entry (matcher position included) so unrelated hooks
// keep firing in their original order.
func upsertHookEntry(entries []hookEntry, newEntry hookEntry) []hookEntry {
	for i, e := range entries {
		if e.Tag == driftHookMarker {
			entries[i] = newEntry
			return entries
		}
	}
	return append(entries, newEntry)
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
