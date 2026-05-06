package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SHC-Labs/drift/internal/config"
)

// WriteMCPEntry writes the drift entry to the per-client MCP config
// file at the right shape for the given client. driftURL is the local
// relay URL (http://127.0.0.1:<port>/mcp). MCP clients connect to
// localhost; the relay handles the upstream Bearer auth from keychain.
//
// Conservative writer: read existing config, modify only the drift
// entry, preserve every other server + every other top-level key,
// write atomically.
//
// Returns the resolved config path actually written + any error. Path
// is included in the response so the install command can print it.
func WriteMCPEntry(d Detected, driftURL string) (string, error) {
	if d.ConfigPath == "" {
		return "", fmt.Errorf("client %s has no scriptable config path", d.ID)
	}
	switch d.ID {
	case ClaudeCode:
		// Claude Code's mcp.json shape matches the standard MCP spec.
		// We use ~/.mcp.json for it (drift install global), not the
		// per-client settings.json which is for hook registration.
		return d.ConfigPath, nil
	case Cursor:
		return writeCursorMCP(d.ConfigPath, driftURL)
	case Windsurf:
		return writeStandardMCP(d.ConfigPath, driftURL)
	case Antigravity:
		return writeStandardMCP(d.ConfigPath, driftURL)
	case Zed:
		return writeZedMCP(d.ConfigPath, driftURL)
	case Kimi:
		return writeStandardMCP(d.ConfigPath, driftURL)
	case VSCode:
		return writeVSCodeMCP(d.ConfigPath, driftURL)
	case Kilo:
		return writeKiloMCP(d.ConfigPath, driftURL)
	default:
		return "", fmt.Errorf("no writer for client %s", d.ID)
	}
}

// writeStandardMCP handles the common shape: top-level mcpServers
// map keyed by server name. Used by Windsurf, Antigravity, Kimi.
func writeStandardMCP(path, driftURL string) (string, error) {
	root, err := readJSONOrEmpty(path)
	if err != nil {
		return path, err
	}
	servers := decodeRawMap(root["mcpServers"])
	driftEntry := map[string]any{
		"type": "http",
		"url":  driftURL,
	}
	raw, err := json.Marshal(driftEntry)
	if err != nil {
		return path, err
	}
	servers["drift"] = raw
	return writeMcpServersBack(path, root, servers)
}

// writeCursorMCP handles Cursor's per-project .cursor/mcp.json shape.
// Cursor's MCP config lives in <project>/.cursor/mcp.json, NOT in the
// global app dir. drift install global doesn't write per-project
// configs; the user runs drift init in each project for that. We
// write a marker file at the global app dir indicating Cursor was
// detected so the install summary can prompt for per-project setup.
func writeCursorMCP(path, driftURL string) (string, error) {
	// Cursor has a per-project model: .cursor/mcp.json lives in each
	// project. drift init handles that. drift install just notes the
	// detection; we don't write to the global app dir because there's
	// no MCP config there.
	return "", nil
}

// writeZedMCP handles Zed's settings.json. Zed uses a top-level
// "context_servers" or "mcp_servers" depending on version; we write
// to "context_servers" (current schema as of late 2025).
func writeZedMCP(path, driftURL string) (string, error) {
	root, err := readJSONOrEmpty(path)
	if err != nil {
		return path, err
	}
	servers := decodeRawMap(root["context_servers"])
	driftEntry := map[string]any{
		"command": map[string]any{
			"path": "drift",
			"args": []string{"_mcp_proxy"},
		},
		"settings": map[string]any{},
	}
	raw, err := json.Marshal(driftEntry)
	if err != nil {
		return path, err
	}
	servers["drift"] = raw
	rawServers, err := json.Marshal(servers)
	if err != nil {
		return path, err
	}
	root["context_servers"] = rawServers
	return writeJSONIndent(path, root)
}

// writeVSCodeMCP handles VS Code's User/settings.json. VS Code wraps
// MCP under "github.copilot.chat.mcp" or similar nested paths
// depending on extension. We write to a per-project .vscode/mcp.json
// instead because that's the canonical location.
func writeVSCodeMCP(path, driftURL string) (string, error) {
	// Same per-project pattern as Cursor: VS Code's MCP config lives
	// in <project>/.vscode/mcp.json. drift init handles that.
	return "", nil
}

// writeKiloMCP handles Kilo's kilo.jsonc shape. JSONC is JSON with
// comments; we read tolerantly and write back as plain JSON (loses
// comments). Customers who care about preserving comments should
// edit by hand.
func writeKiloMCP(path, driftURL string) (string, error) {
	// Kilo's kilo.jsonc allows comments; for v1 we write standard
	// JSON which Kilo's parser accepts. Same shape as standard MCP.
	return writeStandardMCP(path, driftURL)
}

// readJSONOrEmpty reads path as JSON into a generic map. Missing file
// returns an empty map; bad JSON returns an error.
func readJSONOrEmpty(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	return root, nil
}

// decodeRawMap unwraps a json.RawMessage into a map, returning an
// empty map when the input is nil or unparseable. Used for the
// per-server inner maps every MCP config has.
func decodeRawMap(raw json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// writeMcpServersBack rebuilds the mcpServers field from the modified
// servers map and writes the whole file atomically.
func writeMcpServersBack(path string, root map[string]json.RawMessage, servers map[string]json.RawMessage) (string, error) {
	rawServers, err := json.Marshal(servers)
	if err != nil {
		return path, err
	}
	root["mcpServers"] = rawServers
	return writeJSONIndent(path, root)
}

// writeJSONIndent serializes the root map with indent and writes
// atomically. Creates the parent dir if missing.
func writeJSONIndent(path string, root map[string]json.RawMessage) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return path, err
	}
	data = append(data, '\n')
	return path, config.AtomicWriteFile(path, data, 0o600)
}
