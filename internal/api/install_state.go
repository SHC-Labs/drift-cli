package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"
)

// CLIInstalledRequest matches DRIFT_INSTALL_STATE_API_SPEC.md POST
// /api/install/cli-installed. Idempotent on the server: same install_id
// upserts the row.
type CLIInstalledRequest struct {
	InstallID     string `json:"install_id"`
	BinaryVersion string `json:"binary_version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	HostnameHash  string `json:"hostname_hash,omitempty"`
}

// ClientConnectedRequest matches POST /api/install/client-connected.
// Multiple rows allowed per install_id (one per detected MCP client).
type ClientConnectedRequest struct {
	InstallID  string `json:"install_id"`
	Client     string `json:"client"` // claude-code | cursor | windsurf | antigravity | zed | kimi | chatgpt | vscode | kilo
	Success    bool   `json:"success"`
	ConfigPath string `json:"config_path,omitempty"`
}

// RelayEnabledRequest matches POST /api/install/relay-enabled. Fired
// once when the relay binds + completes the upstream handshake.
type RelayEnabledRequest struct {
	InstallID    string `json:"install_id"`
	Transport    string `json:"transport"` // http | unix-socket | named-pipe
	PortOrPath   string `json:"port_or_path"`
}

// RelayHeartbeatRequest matches POST /api/install/relay-heartbeat.
// Fired on a goroutine inside the service every 60s by default. Server
// can override cadence via X-Drift-Heartbeat-Cadence response header.
type RelayHeartbeatRequest struct {
	InstallID     string `json:"install_id"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// HeartbeatResponse carries the optional cadence override server side.
// If CadenceSeconds is non-zero, the relay's heartbeat goroutine
// adopts the new value starting next tick.
type HeartbeatResponse struct {
	CadenceSeconds int
}

// PostCLIInstalled fires the cli-installed state event. Fire-and-forget
// with retry: 3 attempts at 5s/30s/120s backoff. Logs failures via the
// caller's error path; never blocks install on dashboard reachability.
//
// Caller controls retry by calling this in a goroutine; this function
// itself does the SINGLE attempt + returns.
func (c *Client) PostCLIInstalled(ctx context.Context, req CLIInstalledRequest) error {
	return c.postJSON(ctx, "/api/install/cli-installed", req, nil)
}

// PostClientConnected fires per detected MCP client. Multiple calls
// per install allowed.
func (c *Client) PostClientConnected(ctx context.Context, req ClientConnectedRequest) error {
	return c.postJSON(ctx, "/api/install/client-connected", req, nil)
}

// PostRelayEnabled fires once when the relay binds and completes its
// upstream handshake successfully.
func (c *Client) PostRelayEnabled(ctx context.Context, req RelayEnabledRequest) error {
	return c.postJSON(ctx, "/api/install/relay-enabled", req, nil)
}

// PostRelayHeartbeat fires periodically. Captures the optional cadence
// override from the response header and returns it for the caller's
// goroutine to adopt.
func (c *Client) PostRelayHeartbeat(ctx context.Context, req RelayHeartbeatRequest) (*HeartbeatResponse, error) {
	hbResp := &HeartbeatResponse{}
	hookFn := func(resp *http.Response) {
		v := resp.Header.Get("X-Drift-Heartbeat-Cadence")
		if v == "" {
			return
		}
		// strconv.Atoi inline to avoid the import
		var n int
		for _, r := range v {
			if r < '0' || r > '9' {
				return
			}
			n = n*10 + int(r-'0')
		}
		if n > 0 {
			hbResp.CadenceSeconds = n
		}
	}
	if err := c.postJSON(ctx, "/api/install/relay-heartbeat", req, hookFn); err != nil {
		return nil, err
	}
	return hbResp, nil
}

// PostJSON is the public version of postJSON for callers outside the
// install_state event family (relay's pubkey publish, KEK fetch, etc).
// 204 or any 2xx is a success; non-2xx surfaces the structured error.
func (c *Client) PostJSON(ctx context.Context, path string, body any) error {
	return c.postJSON(ctx, path, body, nil)
}

// PostJSONInto is like PostJSON but decodes the response body into
// out. Used for endpoints that return data (DEK fetch, KEK fetch).
func (c *Client) PostJSONInto(ctx context.Context, path string, body, out any) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(path), bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.AddAuth(req)
	c.AddUserAgent(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("post %s: HTTP %d: %s", path, resp.StatusCode, string(bodyBytes))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetJSON is the GET equivalent of PostJSON; decodes the response
// into out. Used for fetching server-side wrapped keys.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path), nil)
	if err != nil {
		return err
	}
	c.AddAuth(req)
	c.AddUserAgent(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("get %s: HTTP %d: %s", path, resp.StatusCode, string(bodyBytes))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// postJSON is the shared POST helper. Marshals body, sets headers,
// expects 204 (or 2xx). On non-2xx returns an error including the
// server's error code + detail per the API spec.
func (c *Client) postJSON(ctx context.Context, path string, body any, respHook func(*http.Response)) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL(path), bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.AddAuth(req)
	c.AddUserAgent(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", path, err)
	}
	defer resp.Body.Close()
	if respHook != nil {
		respHook(resp)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Try to parse the structured error body per the API spec:
	// {"error": "DRIFT_E_*", "detail": "..."}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var apiErr struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(bodyBytes, &apiErr)
	if apiErr.Error != "" {
		return fmt.Errorf("post %s: HTTP %d %s: %s", path, resp.StatusCode, apiErr.Error, apiErr.Detail)
	}
	return fmt.Errorf("post %s: HTTP %d: %s", path, resp.StatusCode, string(bodyBytes))
}

// PostWithRetry runs fn under the standard fire-and-forget retry
// schedule from the plan: 3 attempts at 5s/30s/120s backoff. Used by
// drift install + drift init to fire state events without blocking.
//
// Returns the first success or the last error after all retries
// exhaust. Does NOT block the caller; spawn this in a goroutine if
// you don't want to wait the full retry window.
func PostWithRetry(parent context.Context, fn func(ctx context.Context) error) error {
	delays := []time.Duration{0, 5 * time.Second, 30 * time.Second, 120 * time.Second}
	var lastErr error
	for i, d := range delays {
		if d > 0 {
			select {
			case <-time.After(d):
			case <-parent.Done():
				return parent.Err()
			}
		}
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		err := fn(ctx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		_ = i
	}
	return lastErr
}

// HostnameHash returns SHA-256 of the system hostname, hex-encoded,
// truncated to 16 chars. Used for the optional hostname_hash field
// in cli-installed; never sends raw hostname per PRIVACY.md.
func HostnameHash() string {
	host, err := osHostname()
	if err != nil || host == "" {
		return ""
	}
	return shortHash(host)
}

// runtimeOS returns the runtime.GOOS string in the install state event
// vocabulary (linux, darwin, windows). Just a re-export so callers
// don't import runtime directly.
func RuntimeOS() string { return runtime.GOOS }

// RuntimeArch returns the runtime.GOARCH string (amd64, arm64).
func RuntimeArch() string { return runtime.GOARCH }
