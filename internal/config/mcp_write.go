package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// WriteMCPDriftEntry merges the drift mcpServers entry into ~/.mcp.json.
// Reads the file if it exists, sets mcpServers["drift"] to the new entry,
// preserves every other top-level field and every other mcpServers entry,
// writes atomically.
//
// driftURL is the URL the MCP client connects to: the LOCAL relay
// (http://127.0.0.1:<port>/mcp), not the upstream. The relay handles
// the upstream Bearer auth from the keychain, so this entry has no
// Authorization header at all -- the local relay accepts any
// localhost connection.
func WriteMCPDriftEntry(driftURL string) error {
	return WriteMCPDriftEntryRecovering(driftURL, io.Discard)
}

// WriteMCPDriftEntryRecovering is WriteMCPDriftEntry that prints a
// recovery notice to the supplied writer if it has to back up a corrupt
// ~/.mcp.json. drift install uses this so the customer sees what
// happened. Without this, a corrupt file made install fail mid-flow.
func WriteMCPDriftEntryRecovering(driftURL string, notify io.Writer) error {
	path := MCPPath()
	if path == "" {
		return ErrHomeUnset
	}

	// Read-modify-write the file. Use a generic map so we don't drop
	// other top-level keys that aren't in our struct.
	root := map[string]json.RawMessage{}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			// Corrupt mcp.json (truncated write, manual edit gone wrong).
			// Back up and start fresh rather than failing install.
			backup := fmt.Sprintf("%s.corrupt.%d", path, time.Now().Unix())
			if rerr := os.Rename(path, backup); rerr != nil {
				return fmt.Errorf("parse %s: %w (and could not back up: %v)", path, err, rerr)
			}
			fmt.Fprintf(notify, "drift install: ~/.mcp.json was corrupt; backed up to %s and rebuilding fresh.\n", backup)
			root = map[string]json.RawMessage{}
		}
	}

	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
	}

	driftEntry := map[string]any{
		"type": "http",
		"url":  driftURL,
	}
	driftRaw, err := json.Marshal(driftEntry)
	if err != nil {
		return err
	}
	servers["drift"] = driftRaw

	serversRaw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	root["mcpServers"] = serversRaw

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return AtomicWriteFile(path, out, 0o600)
}
