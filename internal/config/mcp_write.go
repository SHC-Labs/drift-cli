package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// WriteMCPDriftEntry merges the drift mcpServers entry into ~/.mcp.json.
// Reads the file if it exists, sets mcpServers["drift"] to the new entry,
// preserves every other top-level field and every other mcpServers entry,
// writes atomically.
//
// driftURL is the URL the MCP client connects to. In Sprint 2+ this is
// the LOCAL relay (http://127.0.0.1:<port>/mcp), not the upstream. The
// relay handles the upstream Bearer auth from the keychain, so this
// entry has no Authorization header at all -- the local relay accepts
// any localhost connection.
func WriteMCPDriftEntry(driftURL string) error {
	path := MCPPath()

	// Read-modify-write the file. Use a generic map so we don't drop
	// other top-level keys that aren't in our struct.
	root := map[string]json.RawMessage{}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
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
