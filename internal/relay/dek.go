package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/crypto"
	"github.com/SHC-Labs/drift/internal/log"
)

// dekFetchResponse is the wire format from GET /api/relay/dek (org
// DEK) or GET /api/relay/dek/by-project/<project_hash> (per-project
// DEK). wrapped_dek is a SINGLE base64 blob; the layout is:
//
//	nonce(12) || tag(16) || ciphertext(32 for a 32-byte DEK)
//
// Caller splits the blob and AES-GCM-decrypts under the org KEK.
type dekFetchResponse struct {
	OrgID       string `json:"org_id"`
	DEKVersion  int    `json:"dek_version"`
	Fingerprint string `json:"fingerprint"`
	WrappedDEK  string `json:"wrapped_dek"` // base64(nonce||tag||ct)
	CreatedAt   string `json:"created_at"`
	RotatedAt   string `json:"rotated_at,omitempty"`
}

// DEK blob layout. Mirrors the TS relay's wrapDek output: 12-byte
// nonce, 16-byte AES-GCM tag, then the ciphertext (32 bytes for a
// 32-byte DEK plaintext).
const (
	dekBlobNonceBytes = 12
	dekBlobTagBytes   = 16
	dekBlobMinBytes   = dekBlobNonceBytes + dekBlobTagBytes
)

// DEKManager owns the org DEK lifecycle. Caches up to N versions in
// memory keyed by version int. The encryption pipeline always uses
// the latest; the decryption pipeline looks up version from the
// envelope's dek_id field.
//
// Self-heal mirrors the KEK manager: a 404 from GET /api/relay/dek
// triggers a fresh-provision; an auth-fail unwrap (typically after a
// KEK rotation seeded a fresh KEK that no longer decrypts the
// server-stored wrap) triggers a rotate-and-replace under the current
// KEK so the relay comes back online without operator intervention.
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

	dek, version, err := m.fetchOrProvision(ctx)
	if err != nil {
		return nil, 0, err
	}
	m.cache[version] = dek
	m.current = version
	m.expiry = time.Now().Add(1 * time.Hour)
	return append([]byte{}, dek...), version, nil
}

// ByVersion returns the DEK for the given version, fetching from
// server if not cached. Used by the decryption pipeline when it sees
// an envelope with dek_id != current.
//
// No self-heal here: a specific historical version that fails to
// unwrap means the data referenced by that envelope is unrecoverable
// (KEK rotation already destroyed the wrap that protected it). Caller
// surfaces the failure so the operator sees corrupt-content rather
// than a silent rotate that would mask data loss.
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
	path := fmt.Sprintf("/api/relay/dek?version=%d", version)
	dek, _, err := m.fetchAndUnwrap(ctx, path)
	if err != nil {
		return nil, err
	}
	m.cache[version] = dek
	return append([]byte{}, dek...), nil
}

// fetchOrProvision is the self-healing fetch path used by Current().
// Distinct from fetchAndUnwrap because the recovery branches (404 ->
// provision, auth-fail -> rotate) only make sense for the latest DEK.
//
// Caller MUST hold m.mu.Lock().
func (m *DEKManager) fetchOrProvision(ctx context.Context) ([]byte, int, error) {
	var resp dekFetchResponse
	err := m.client.GetJSON(ctx, "/api/relay/dek", &resp)
	if err != nil {
		if isHTTP404(err) {
			log.Info("relay.dek", "no_dek_provisioning_fresh", nil)
			return m.provisionFreshDEK(ctx, false)
		}
		return nil, 0, fmt.Errorf("fetch DEK: %w", err)
	}

	dek, err := unwrapDekBlob(resp.WrappedDEK, m, ctx)
	if err != nil {
		if errors.Is(err, crypto.ErrAuthFailed) {
			// The wrapped DEK on the server can't be decrypted under
			// the current KEK, typically because a prior session
			// triggered a KEK self-heal (Magnum -> v0.1.10 path) and
			// the DEK is still wrapped under the dead KEK. Rotate the
			// DEK so the relay is usable again. Existing envelopes
			// that referenced the old DEK_version remain unrecoverable
			// (KEK rotation already invalidated the chain protecting
			// them); ByVersion() reports those as decrypt errors so
			// the operator sees the data loss rather than a silent
			// substitution.
			log.Warn("relay.dek", "stale_wrap_detected", map[string]any{
				"stale_version":   resp.DEKVersion,
				"stale_fp_remote": resp.Fingerprint,
			})
			return m.provisionFreshDEK(ctx, true)
		}
		return nil, 0, err
	}
	log.Info("relay.dek", "unwrapped_current", map[string]any{
		"version":     resp.DEKVersion,
		"fingerprint": crypto.Fingerprint(dek),
	})
	return dek, resp.DEKVersion, nil
}

// fetchAndUnwrap is the historical-fetch path used by ByVersion.
// Returns the raw DEK + version with no self-heal; caller decides
// whether to surface the failure or recover.
func (m *DEKManager) fetchAndUnwrap(ctx context.Context, path string) ([]byte, int, error) {
	var resp dekFetchResponse
	if err := m.client.GetJSON(ctx, path, &resp); err != nil {
		return nil, 0, fmt.Errorf("fetch DEK: %w", err)
	}

	dek, err := unwrapDekBlob(resp.WrappedDEK, m, ctx)
	if err != nil {
		return nil, 0, err
	}
	return dek, resp.DEKVersion, nil
}

// provisionFreshDEK generates a fresh 32-byte DEK, wraps it under the
// current KEK, and POSTs to /api/relay/dek. When rotate is true the
// server replaces the existing DEK at a bumped version; when false the
// server inserts at version 1 (and 409s if a DEK already exists, which
// shouldn't happen in the rotate=false path because the caller only
// hits it after a 404).
//
// Caller MUST hold m.mu.Lock().
func (m *DEKManager) provisionFreshDEK(ctx context.Context, rotate bool) ([]byte, int, error) {
	kek, err := m.kek.Get(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("provision DEK: get KEK: %w", err)
	}

	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, 0, fmt.Errorf("provision DEK: gen entropy: %w", err)
	}

	wrappedB64, err := wrapDekUnderKEK(dek, kek)
	if err != nil {
		return nil, 0, fmt.Errorf("provision DEK: wrap: %w", err)
	}

	body := map[string]any{
		"wrapped_dek": wrappedB64,
		"fingerprint": crypto.Fingerprint(dek),
	}
	path := "/api/relay/dek"
	if rotate {
		path = "/api/relay/dek?rotate=true"
	}
	var resp dekFetchResponse
	if err := m.client.PostJSONInto(ctx, path, body, &resp); err != nil {
		return nil, 0, fmt.Errorf("provision DEK: POST %s: %w", path, err)
	}

	log.Info("relay.dek", "provisioned", map[string]any{
		"version":     resp.DEKVersion,
		"fingerprint": crypto.Fingerprint(dek),
		"rotated":     rotate,
	})
	return dek, resp.DEKVersion, nil
}

// unwrapDekBlob splits the base64 wrapped_dek blob into nonce + tag +
// ciphertext, fetches the org KEK, and AES-GCM-decrypts. Mirrors the
// TS relay's unwrapDek() byte layout: nonce(12) || tag(16) || ct.
func unwrapDekBlob(wrappedB64 string, m *DEKManager, ctx context.Context) ([]byte, error) {
	wrapped, err := base64.StdEncoding.DecodeString(wrappedB64)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped_dek: %w", err)
	}
	if len(wrapped) < dekBlobMinBytes {
		return nil, fmt.Errorf("wrapped_dek too short (%d bytes, want >=%d)", len(wrapped), dekBlobMinBytes)
	}
	nonce := wrapped[:dekBlobNonceBytes]
	tag := wrapped[dekBlobNonceBytes : dekBlobNonceBytes+dekBlobTagBytes]
	ct := wrapped[dekBlobNonceBytes+dekBlobTagBytes:]

	kek, err := m.kek.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("get KEK: %w", err)
	}

	aead, err := crypto.NewAESGCM256(kek)
	if err != nil {
		return nil, err
	}
	cipherWithTag := append(append([]byte{}, ct...), tag...)
	dek, err := aead.Open(cipherWithTag, nonce, nil)
	if err != nil {
		return nil, err
	}
	if len(dek) != 32 {
		return nil, fmt.Errorf("unwrapped DEK length %d, want 32", len(dek))
	}
	return dek, nil
}

// wrapDekUnderKEK is the inverse of unwrapDekBlob: encrypt a 32-byte
// DEK under a 32-byte KEK and emit the base64 of nonce||tag||ct in the
// wire layout the server expects.
func wrapDekUnderKEK(dek, kek []byte) (string, error) {
	if len(dek) != 32 {
		return "", fmt.Errorf("DEK must be 32 bytes, got %d", len(dek))
	}
	if len(kek) != 32 {
		return "", fmt.Errorf("KEK must be 32 bytes, got %d", len(kek))
	}
	aead, err := crypto.NewAESGCM256(kek)
	if err != nil {
		return "", err
	}
	nonce, err := crypto.RandomNonce()
	if err != nil {
		return "", err
	}
	ctWithTag, err := aead.Seal(dek, nonce, nil)
	if err != nil {
		return "", err
	}
	if len(ctWithTag) < dekBlobTagBytes {
		return "", fmt.Errorf("seal output too short (%d bytes)", len(ctWithTag))
	}
	// Go's AEAD.Seal returns ciphertext||tag; the wire format is
	// nonce||tag||ciphertext, so split and reorder.
	ct := ctWithTag[:len(ctWithTag)-dekBlobTagBytes]
	tag := ctWithTag[len(ctWithTag)-dekBlobTagBytes:]
	out := make([]byte, 0, dekBlobNonceBytes+dekBlobTagBytes+len(ct))
	out = append(out, nonce...)
	out = append(out, tag...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// ProjectDEKManager handles per-project DEKs. Per-project encryption
// uses a separate DEK keyed by project_hash so projects are
// cryptographically isolated within an org. URL pattern from the TS
// server: /api/relay/dek/by-project/<project_hash>.
type ProjectDEKManager struct {
	client *api.Client
	kek    *KEKManager
	dek    *DEKManager // for org KEK access via the same chain

	mu    sync.RWMutex
	cache map[string]projectDEKEntry // project_hash -> entry
}

type projectDEKEntry struct {
	DEK     []byte
	Version int
	Expiry  time.Time
}

// NewProjectDEKManager builds the per-project DEK cache.
func NewProjectDEKManager(client *api.Client, kek *KEKManager, orgDek *DEKManager) *ProjectDEKManager {
	return &ProjectDEKManager{
		client: client,
		kek:    kek,
		dek:    orgDek,
		cache:  map[string]projectDEKEntry{},
	}
}

// Get returns the per-project DEK for the given project_hash,
// fetching + unwrapping on cache miss. Same 1-hour TTL as the org
// DEK; rotation invalidates via Drop.
//
// Self-heal mirrors DEKManager.Current: a 404 means the project DEK
// was never provisioned (first call after `drift project enable`), so
// we generate a fresh one and POST. An auth-fail unwrap means the
// project DEK is wrapped under a stale KEK (post KEK self-heal) and
// we rotate.
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

	dek, version, err := m.fetchOrProvision(ctx, projectHash)
	if err != nil {
		return nil, 0, err
	}
	m.cache[projectHash] = projectDEKEntry{
		DEK:     dek,
		Version: version,
		Expiry:  time.Now().Add(1 * time.Hour),
	}
	return append([]byte{}, dek...), version, nil
}

// fetchOrProvision is the project-DEK equivalent of
// DEKManager.fetchOrProvision. Same recovery strategy: 404 =
// provision, auth-fail = rotate. Caller MUST hold m.mu.Lock().
func (m *ProjectDEKManager) fetchOrProvision(ctx context.Context, projectHash string) ([]byte, int, error) {
	getPath := fmt.Sprintf("/api/relay/dek/by-project/%s", projectHash)
	var resp dekFetchResponse
	err := m.client.GetJSON(ctx, getPath, &resp)
	if err != nil {
		if isHTTP404(err) {
			log.Info("relay.dek", "project_no_dek_provisioning_fresh", map[string]any{
				"project_hash": projectHash,
			})
			return m.provisionFreshProjectDEK(ctx, projectHash, false)
		}
		return nil, 0, err
	}

	dek, err := unwrapDekBlob(resp.WrappedDEK, m.dek, ctx)
	if err != nil {
		if errors.Is(err, crypto.ErrAuthFailed) {
			log.Warn("relay.dek", "project_stale_wrap_detected", map[string]any{
				"project_hash":    projectHash,
				"stale_version":   resp.DEKVersion,
				"stale_fp_remote": resp.Fingerprint,
			})
			return m.provisionFreshProjectDEK(ctx, projectHash, true)
		}
		return nil, 0, err
	}
	log.Info("relay.dek", "unwrapped_project", map[string]any{
		"project_hash": projectHash,
		"version":      resp.DEKVersion,
	})
	return dek, resp.DEKVersion, nil
}

// provisionFreshProjectDEK is the per-project equivalent of
// DEKManager.provisionFreshDEK. Same wire format on POST; the URL
// path differs and the server enforces project-membership separately.
//
// Caller MUST hold m.mu.Lock().
func (m *ProjectDEKManager) provisionFreshProjectDEK(ctx context.Context, projectHash string, rotate bool) ([]byte, int, error) {
	kek, err := m.kek.Get(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("provision project DEK: get KEK: %w", err)
	}

	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, 0, fmt.Errorf("provision project DEK: gen entropy: %w", err)
	}

	wrappedB64, err := wrapDekUnderKEK(dek, kek)
	if err != nil {
		return nil, 0, fmt.Errorf("provision project DEK: wrap: %w", err)
	}

	body := map[string]any{
		"wrapped_dek": wrappedB64,
		"fingerprint": crypto.Fingerprint(dek),
	}
	path := fmt.Sprintf("/api/relay/dek/by-project/%s", projectHash)
	if rotate {
		path = path + "?rotate=true"
	}
	var resp dekFetchResponse
	if err := m.client.PostJSONInto(ctx, path, body, &resp); err != nil {
		return nil, 0, fmt.Errorf("provision project DEK: POST %s: %w", path, err)
	}

	log.Info("relay.dek", "project_provisioned", map[string]any{
		"project_hash": projectHash,
		"version":      resp.DEKVersion,
		"fingerprint":  crypto.Fingerprint(dek),
		"rotated":      rotate,
	})
	return dek, resp.DEKVersion, nil
}

// Drop clears the cached entry for project_hash. Called on KEK
// rotation (invalidates everything) or per-project rotation (specific
// hash only).
func (m *ProjectDEKManager) Drop(projectHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, projectHash)
}

// decodeHexPubkey turns a hex-encoded ECDH pubkey back into bytes.
// 65 byte uncompressed SEC1 = 130 hex chars. Kept for callers that
// pass hex-encoded pubkeys; the TS server uses base64 on the wire,
// so most call sites use base64 directly.
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
