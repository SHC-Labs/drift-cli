package relay

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/crypto"
	"github.com/SHC-Labs/drift/internal/log"
)

// dekFetchResponse is the wire format from /v1/relay/dek (org DEK)
// or /v1/relay/dek/<project_hash> (per-project DEK). Server returns
// the wrapped DEK + nonce + tag; client unwraps with the org KEK.
type dekFetchResponse struct {
	WrappedDEK string `json:"wrapped_dek"`
	Nonce      string `json:"nonce"`
	Tag        string `json:"tag"`
	Version    int    `json:"version"`
}

// DEKManager owns the org DEK lifecycle. Caches up to N versions in
// memory keyed by version int; the encryption pipeline always uses
// the latest, the decryption pipeline looks up the version from the
// envelope's dek_id field.
type DEKManager struct {
	client *api.Client
	kek    *KEKManager

	mu      sync.RWMutex
	cache   map[int][]byte // version -> 32-byte DEK
	current int            // version of the latest fetched DEK
	expiry  time.Time
}

// NewDEKManager builds a manager that fetches under the given KEK.
func NewDEKManager(client *api.Client, kek *KEKManager) *DEKManager {
	return &DEKManager{
		client: client,
		kek:    kek,
		cache:  map[int][]byte{},
	}
}

// Current returns the latest org DEK, fetching + unwrapping on first
// call or cache miss. Caches for 1 hour to bound DEK fetches under
// high MCP traffic.
func (m *DEKManager) Current(ctx context.Context) ([]byte, int, error) {
	m.mu.RLock()
	if m.current != 0 && time.Now().Before(m.expiry) {
		dek := append([]byte{}, m.cache[m.current]...)
		ver := m.current
		m.mu.RUnlock()
		return dek, ver, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != 0 && time.Now().Before(m.expiry) {
		return append([]byte{}, m.cache[m.current]...), m.current, nil
	}

	dek, version, err := m.fetchAndUnwrap(ctx, "/v1/relay/dek")
	if err != nil {
		return nil, 0, err
	}
	m.cache[version] = dek
	m.current = version
	m.expiry = time.Now().Add(1 * time.Hour)
	log.Info("relay.dek", "unwrapped_current", map[string]any{
		"version":     version,
		"fingerprint": crypto.Fingerprint(dek),
	})
	return append([]byte{}, dek...), version, nil
}

// ByVersion returns the DEK for the given version, fetching from
// server if not cached. Used by the decryption pipeline when it sees
// an envelope with dek_id != current.
func (m *DEKManager) ByVersion(ctx context.Context, version int) ([]byte, error) {
	m.mu.RLock()
	if dek, ok := m.cache[version]; ok {
		out := append([]byte{}, dek...)
		m.mu.RUnlock()
		return out, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if dek, ok := m.cache[version]; ok {
		return append([]byte{}, dek...), nil
	}
	path := fmt.Sprintf("/v1/relay/dek?version=%d", version)
	dek, _, err := m.fetchAndUnwrap(ctx, path)
	if err != nil {
		return nil, err
	}
	m.cache[version] = dek
	return append([]byte{}, dek...), nil
}

// fetchAndUnwrap is the shared path: GET endpoint, unwrap with KEK,
// return the raw DEK bytes + version.
func (m *DEKManager) fetchAndUnwrap(ctx context.Context, path string) ([]byte, int, error) {
	var resp dekFetchResponse
	if err := m.client.GetJSON(ctx, path, &resp); err != nil {
		return nil, 0, fmt.Errorf("fetch DEK: %w", err)
	}

	wrapped, err := base64.StdEncoding.DecodeString(resp.WrappedDEK)
	if err != nil {
		return nil, 0, fmt.Errorf("decode wrapped_dek: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(resp.Nonce)
	if err != nil {
		return nil, 0, fmt.Errorf("decode nonce: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(resp.Tag)
	if err != nil {
		return nil, 0, fmt.Errorf("decode tag: %w", err)
	}

	kek, err := m.kek.Get(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("get KEK: %w", err)
	}

	aead, err := crypto.NewAESGCM256(kek)
	if err != nil {
		return nil, 0, err
	}
	cipherWithTag := append(append([]byte{}, wrapped...), tag...)
	dek, err := aead.Open(cipherWithTag, nonce, nil)
	if err != nil {
		// Treat as KEK invalid + retry once after invalidating.
		m.kek.Invalidate()
		return nil, 0, fmt.Errorf("unwrap DEK: %w", err)
	}
	if len(dek) != 32 {
		return nil, 0, fmt.Errorf("unwrapped DEK length %d, want 32", len(dek))
	}
	return dek, resp.Version, nil
}

// ProjectDEKManager handles per-project DEKs. Per-project encryption
// uses a separate DEK keyed by project_hash so projects are
// cryptographically isolated within an org.
type ProjectDEKManager struct {
	client *api.Client
	kek    *KEKManager

	mu    sync.RWMutex
	cache map[string]projectDEKEntry // project_hash -> entry
}

type projectDEKEntry struct {
	DEK     []byte
	Version int
	Expiry  time.Time
}

// NewProjectDEKManager builds the per-project DEK cache.
func NewProjectDEKManager(client *api.Client, kek *KEKManager) *ProjectDEKManager {
	return &ProjectDEKManager{
		client: client,
		kek:    kek,
		cache:  map[string]projectDEKEntry{},
	}
}

// Get returns the per-project DEK for the given project_hash,
// fetching + unwrapping on cache miss. Same 1-hour TTL as the org
// DEK; rotation invalidates via Drop.
func (m *ProjectDEKManager) Get(ctx context.Context, projectHash string) ([]byte, int, error) {
	if projectHash == "" {
		return nil, 0, fmt.Errorf("project_hash required")
	}
	m.mu.RLock()
	if entry, ok := m.cache[projectHash]; ok && time.Now().Before(entry.Expiry) {
		dek := append([]byte{}, entry.DEK...)
		ver := entry.Version
		m.mu.RUnlock()
		return dek, ver, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.cache[projectHash]; ok && time.Now().Before(entry.Expiry) {
		return append([]byte{}, entry.DEK...), entry.Version, nil
	}

	path := fmt.Sprintf("/v1/relay/project-dek/%s", projectHash)
	dek, version, err := m.fetchAndUnwrap(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	m.cache[projectHash] = projectDEKEntry{
		DEK:     dek,
		Version: version,
		Expiry:  time.Now().Add(1 * time.Hour),
	}
	log.Info("relay.dek", "unwrapped_project", map[string]any{
		"project_hash": projectHash,
		"version":      version,
	})
	return append([]byte{}, dek...), version, nil
}

// Drop clears the cached entry for project_hash. Called on KEK
// rotation (invalidates everything) or per-project rotation (specific
// hash only).
func (m *ProjectDEKManager) Drop(projectHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, projectHash)
}

// fetchAndUnwrap is the same path as the org DEK manager's; pulled
// into its own method here so per-project additions stay isolated.
func (m *ProjectDEKManager) fetchAndUnwrap(ctx context.Context, path string) ([]byte, int, error) {
	var resp dekFetchResponse
	if err := m.client.GetJSON(ctx, path, &resp); err != nil {
		return nil, 0, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(resp.WrappedDEK)
	if err != nil {
		return nil, 0, fmt.Errorf("decode wrapped_dek: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(resp.Nonce)
	if err != nil {
		return nil, 0, fmt.Errorf("decode nonce: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(resp.Tag)
	if err != nil {
		return nil, 0, fmt.Errorf("decode tag: %w", err)
	}
	kek, err := m.kek.Get(ctx)
	if err != nil {
		return nil, 0, err
	}
	aead, err := crypto.NewAESGCM256(kek)
	if err != nil {
		return nil, 0, err
	}
	cipherWithTag := append(append([]byte{}, wrapped...), tag...)
	dek, err := aead.Open(cipherWithTag, nonce, nil)
	if err != nil {
		m.kek.Invalidate()
		return nil, 0, fmt.Errorf("unwrap project DEK: %w", err)
	}
	return dek, resp.Version, nil
}

// decodeHexPubkey turns a hex-encoded ECDH pubkey back into bytes.
// 65 byte uncompressed SEC1 = 130 hex chars.
func decodeHexPubkey(hexStr string) ([]byte, error) {
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decode hex pubkey: %w", err)
	}
	if len(b) != crypto.ECDHPubKeyBytes {
		return nil, fmt.Errorf("pubkey length %d, want %d", len(b), crypto.ECDHPubKeyBytes)
	}
	return b, nil
}
