package keychain

import (
	"crypto/rand"
	"fmt"
)

// EnsureInstallID returns the existing install_id from the keychain or
// generates + persists a fresh UUIDv4 if none exists. Idempotent: re-runs
// of drift install end up with the same install_id.
//
// Falls back to no-op-with-error if the keychain is unavailable; the
// caller (drift install) decides whether to proceed without an id.
func EnsureInstallID() (string, error) {
	id, err := GetInstallID()
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && err != ErrNotFound {
		// Keychain failure other than not-found: surface to caller
		// so they can decide between proceeding without an id or
		// aborting.
		return "", err
	}
	// First install on this machine: generate UUIDv4 + persist.
	newID, err := generateUUIDv4()
	if err != nil {
		return "", fmt.Errorf("generate install_id: %w", err)
	}
	if err := SetInstallID(newID); err != nil {
		return "", fmt.Errorf("persist install_id: %w", err)
	}
	return newID, nil
}

// generateUUIDv4 returns a fresh RFC 4122 v4 UUID. Uses crypto/rand
// for entropy. ~10 lines vs adding the google/uuid dep.
func generateUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// Version 4 in the top 4 bits of byte 6.
	b[6] = (b[6] & 0x0f) | 0x40
	// Variant 10 (RFC 4122) in the top 2 bits of byte 8.
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
