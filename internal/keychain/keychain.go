// Package keychain wraps github.com/zalando/go-keyring so the binary
// stores secrets in the OS keystore (Keychain on macOS, Credential
// Manager on Windows, Secret Service on Linux) instead of plaintext
// config files.
//
// Stores three things keyed by ItemKey* constants:
//   - Token (drift_<base64url>) the Bearer key for upstream Drift server
//   - InstallID (UUID) the anonymous machine identifier for state events
//   - PrivKeyHex (64 hex chars) the customer's ECDH privkey for KEK wrap
//
// drift login + drift logout drive the lifecycle.
package keychain

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// ServiceName is the keychain service identifier. macOS Keychain uses
// it as the "service" attribute, Windows Credential Manager uses it as
// the target name, Linux Secret Service uses it as the schema attribute.
const ServiceName = "drift"

// Item keys for the three secrets we store.
const (
	ItemKeyToken     = "token"
	ItemKeyInstallID = "install_id"
	ItemKeyPrivKey   = "ecdh_privkey_hex"
)

// ErrNotFound is returned when a key isn't present in the keystore.
// Wraps the underlying library's ErrNotFound so callers don't need to
// import zalando.
var ErrNotFound = errors.New("keychain: item not found")

// Set stores value under key in the keychain. Overwrites any existing
// value silently. On Linux, when the Secret Service is unreachable
// (headless box, SSH without gnome-keyring, container, CI), routes to
// the encrypted ~/.drift/.secrets fallback instead so the binary still
// works. macOS + Windows: keyring is always available, no fallback.
func Set(key, value string) error {
	err := keyring.Set(ServiceName, key, value)
	if err == nil {
		return nil
	}
	if fb := fallbackBackend(); fb != nil && isKeyringUnavailable(err) {
		warnFallback()
		if ferr := fb.Set(key, value); ferr != nil {
			return fmt.Errorf("keychain set %s (fallback): %w", key, ferr)
		}
		return nil
	}
	return fmt.Errorf("keychain set %s: %w", key, err)
}

// Get retrieves the value for key. Returns ErrNotFound if the key
// doesn't exist; other errors come from the platform layer (locked
// keychain, permission denied, etc). Same Linux-headless fallback as
// Set: a keyring "no service" error retries against the encrypted
// file, and a fresh-install file-fallback box returns ErrNotFound
// cleanly when the file doesn't exist yet.
func Get(key string) (string, error) {
	v, err := keyring.Get(ServiceName, key)
	if err == nil {
		return v, nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if fb := fallbackBackend(); fb != nil && isKeyringUnavailable(err) {
		warnFallback()
		v, ferr := fb.Get(key)
		if ferr != nil {
			if errors.Is(ferr, ErrNotFound) {
				return "", ErrNotFound
			}
			return "", fmt.Errorf("keychain get %s (fallback): %w", key, ferr)
		}
		return v, nil
	}
	return "", fmt.Errorf("keychain get %s: %w", key, err)
}

// Delete removes key from the keychain. Idempotent: deleting a missing
// key is not an error. Same fallback behavior as Set/Get on Linux.
func Delete(key string) error {
	err := keyring.Delete(ServiceName, key)
	if err == nil {
		return nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if fb := fallbackBackend(); fb != nil && isKeyringUnavailable(err) {
		warnFallback()
		if ferr := fb.Delete(key); ferr != nil {
			return fmt.Errorf("keychain delete %s (fallback): %w", key, ferr)
		}
		return nil
	}
	return fmt.Errorf("keychain delete %s: %w", key, err)
}

// Convenience wrappers around the three stored items so callers can be
// type-safe and not pass raw keys around.

// SetToken stores the Bearer token.
func SetToken(token string) error { return Set(ItemKeyToken, token) }

// GetToken retrieves the Bearer token.
func GetToken() (string, error) { return Get(ItemKeyToken) }

// DeleteToken removes the Bearer token (drift logout).
func DeleteToken() error { return Delete(ItemKeyToken) }

// SetInstallID stores the install_id UUID.
func SetInstallID(id string) error { return Set(ItemKeyInstallID, id) }

// GetInstallID retrieves the install_id UUID.
func GetInstallID() (string, error) { return Get(ItemKeyInstallID) }

// DeleteInstallID removes the install_id (drift uninstall).
func DeleteInstallID() error { return Delete(ItemKeyInstallID) }

// SetPrivKey stores the customer's ECDH privkey as hex.
func SetPrivKey(hexStr string) error { return Set(ItemKeyPrivKey, hexStr) }

// GetPrivKey retrieves the customer's ECDH privkey.
func GetPrivKey() (string, error) { return Get(ItemKeyPrivKey) }

// DeletePrivKey removes the customer's ECDH privkey (drift uninstall
// or KEK rotation).
func DeletePrivKey() error { return Delete(ItemKeyPrivKey) }
