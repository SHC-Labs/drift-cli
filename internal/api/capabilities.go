// Package api is the HTTP client for the Drift dashboard server.
// Includes the OAuth PKCE login flow, the capability negotiation
// handshake, and the four install state event POSTs.
//
// Every request hits a /v1/-prefixed URL with the User-Agent header
// drift/<version> (os/arch). v2 endpoints land at /v2/ without
// breaking v1 binaries already in the wild.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/SHC-Labs/drift/internal/version"
)

// ClientCapabilities is what this binary advertises to the server on
// every handshake. Mirrors DRIFT_BINARY_REWRITE_PLAN.md "Capability
// negotiation handshake".
type ClientCapabilities struct {
	ProtocolVersions []string `json:"protocol_versions"`
	AEADAlgorithms   []string `json:"aead_algorithms"`
	Features         []string `json:"features"`
	CLIVersion       string   `json:"cli_version"`
	OSArch           string   `json:"os_arch"`
}

// ServerCapabilities is what the server returns. Binary picks the
// strongest match it supports from PreferredAEAD; turns features on
// based on EnabledFeatures; respects MinClientVersion as a force-
// upgrade signal.
type ServerCapabilities struct {
	PreferredAEAD    string   `json:"preferred_aead"`
	EnabledFeatures  []string `json:"enabled_features"`
	MinClientVersion string   `json:"min_client_version,omitempty"`
	DeprecationFlags []string `json:"deprecation_flags,omitempty"`
}

// MyCapabilities returns the capabilities this binary advertises.
// Pulled from internal/version so the build-time-injected values
// surface here too.
func MyCapabilities() ClientCapabilities {
	return ClientCapabilities{
		ProtocolVersions: version.ProtocolVersions,
		AEADAlgorithms:   version.AEADAlgorithms,
		Features:         []string{"state-events", "hook-prompt-submit", "hook-post-tool-use"},
		CLIVersion:       version.Version,
		OSArch:           runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// HandshakeResult is what callers get back. Includes the negotiated
// AEAD and the feature set the server enabled for this client.
type HandshakeResult struct {
	NegotiatedAEAD  string
	EnabledFeatures map[string]bool
	ServerCaps      ServerCapabilities
}

// Has returns true if the named feature is enabled by the server for
// this client. Cheap lookup against the parsed feature set.
func (h HandshakeResult) Has(feature string) bool {
	return h.EnabledFeatures[feature]
}

// Handshake performs the capability negotiation against the server.
// POST /api/capabilities with this binary's capabilities; server returns
// its preferences and the feature set enabled for this client.
//
// Cached for the lifetime of the process: handshakes are stable across
// many MCP requests, no need to renegotiate per-request. Cache busts
// on process restart, which means service restart triggers a fresh
// handshake -- right behavior because that's when we'd want to pick up
// new server-side rollouts.
func Handshake(ctx context.Context, client *Client) (*HandshakeResult, error) {
	cached := getCachedHandshake()
	if cached != nil {
		return cached, nil
	}

	caps := MyCapabilities()
	body, err := json.Marshal(caps)
	if err != nil {
		return nil, fmt.Errorf("marshal capabilities: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.URL("/api/capabilities"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client.AddAuth(req)
	client.AddUserAgent(req)

	resp, err := client.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("handshake request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Server hasn't deployed /api/capabilities yet. Fall back to
		// the binary's defaults: AES-GCM-256 only, no extra features.
		// This keeps the binary working against older servers during
		// the rollout window.
		fallback := &HandshakeResult{
			NegotiatedAEAD:  "aes-gcm-256",
			EnabledFeatures: map[string]bool{},
			ServerCaps:      ServerCapabilities{PreferredAEAD: "aes-gcm-256"},
		}
		setCachedHandshake(fallback)
		return fallback, nil
	}
	if resp.StatusCode != http.StatusOK {
		blob, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("handshake HTTP %d: %s", resp.StatusCode, string(blob))
	}

	var srvCaps ServerCapabilities
	if err := json.NewDecoder(resp.Body).Decode(&srvCaps); err != nil {
		return nil, fmt.Errorf("decode server capabilities: %w", err)
	}

	// Negotiate AEAD: server's preferred wins if we support it,
	// otherwise fall back to AES-GCM-256 (which v1 always supports).
	negotiated := "aes-gcm-256"
	for _, alg := range version.AEADAlgorithms {
		if alg == srvCaps.PreferredAEAD {
			negotiated = srvCaps.PreferredAEAD
			break
		}
	}

	enabled := make(map[string]bool, len(srvCaps.EnabledFeatures))
	for _, f := range srvCaps.EnabledFeatures {
		enabled[f] = true
	}

	result := &HandshakeResult{
		NegotiatedAEAD:  negotiated,
		EnabledFeatures: enabled,
		ServerCaps:      srvCaps,
	}
	setCachedHandshake(result)
	return result, nil
}

// In-process handshake cache. Atomic enough for our usage pattern:
// the relay serializes startup, then concurrent MCP request handlers
// share the cached result via a mutex.
var (
	handshakeMu     sync.Mutex
	handshakeCache  *HandshakeResult
	handshakeExpiry time.Time
)

const handshakeCacheTTL = 24 * time.Hour

func getCachedHandshake() *HandshakeResult {
	handshakeMu.Lock()
	defer handshakeMu.Unlock()
	if handshakeCache != nil && time.Now().Before(handshakeExpiry) {
		return handshakeCache
	}
	return nil
}

func setCachedHandshake(r *HandshakeResult) {
	handshakeMu.Lock()
	defer handshakeMu.Unlock()
	handshakeCache = r
	handshakeExpiry = time.Now().Add(handshakeCacheTTL)
}
