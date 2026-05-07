package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// MCPConfig is the subset of ~/.mcp.json drift cares about. The file may
// hold many other servers; we read mcpServers["drift"] only and leave the
// rest alone.
type MCPConfig struct {
	Token   string // Bearer token from Authorization header (with "Bearer " stripped)
	BaseURL string // The MCP server URL, scheme + host + port (no path, no trailing slash)
	Path    string // Absolute path the config was read from
}

// MCPFile is the on-disk shape, used for read + write. We only touch the
// drift entry under mcpServers; other keys round-trip unmodified via the
// json.RawMessage map.
type MCPFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Other      map[string]json.RawMessage `json:"-"` // populated by ReadMCPRaw, not used by ReadMCP
}

type driftServerEntry struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// MCPPath returns the user's ~/.mcp.json path. Pulled into a function so
// tests can override via $HOME.
func MCPPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mcp.json")
}

// ReadMCP loads ~/.mcp.json and extracts the drift token + server URL. The
// loud-failure caller (the hook) needs to distinguish missing-file from
// missing-token from placeholder-token, so the error wraps a sentinel for
// each case.
func ReadMCP() (*MCPConfig, error) {
	path := MCPPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrMCPMissing
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var raw MCPFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrMCPCorrupt, path, err)
	}

	driftRaw, ok := raw.MCPServers["drift"]
	if !ok {
		return nil, ErrDriftServerMissing
	}
	var entry driftServerEntry
	if err := json.Unmarshal(driftRaw, &entry); err != nil {
		return nil, fmt.Errorf("parse mcpServers.drift: %w", err)
	}

	// Authorization header in mcp.json is OPTIONAL in local-relay mode.
	// The relay adds the Bearer header from the keychain on outbound
	// to upstream; the inbound header from the MCP client to the local
	// relay isn't needed. Legacy mcp.json files that still have Bearer
	// in here just round-trip through ReadMCP for callers that want it.
	authz := entry.Headers["Authorization"]
	token := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	if token != "" && strings.Contains(token, "YOUR_DRIFT_TOKEN") {
		return nil, ErrTokenPlaceholder
	}

	if entry.URL == "" {
		return nil, ErrURLMissing
	}
	base, err := baseURL(entry.URL)
	if err != nil {
		return nil, fmt.Errorf("parse mcp url %q: %w", entry.URL, err)
	}

	return &MCPConfig{
		Token:   token,
		BaseURL: base,
		Path:    path,
	}, nil
}

// baseURL strips the path off an MCP URL like "https://mcp.driftlabs.io/mcp"
// down to "https://mcp.driftlabs.io". Hooks call other endpoints under the
// same origin (e.g. /api/check-updates), so we want the origin only.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("missing scheme or host in %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Sentinel errors so the hook can branch on the failure mode and emit the
// right loud-context message.
var (
	ErrMCPMissing         = errors.New("~/.mcp.json missing")
	ErrMCPCorrupt         = errors.New("~/.mcp.json corrupt")
	ErrDriftServerMissing = errors.New("drift entry missing from mcpServers")
	ErrTokenMissing       = errors.New("no Drift token in ~/.mcp.json")
	ErrTokenPlaceholder   = errors.New("token is the YOUR_DRIFT_TOKEN placeholder")
	ErrURLMissing         = errors.New("no Drift URL in ~/.mcp.json")
)
