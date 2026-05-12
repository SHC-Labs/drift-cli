// W9.2: Linux Secret Service can be missing in headless environments
// (SSH session with no graphical login, docker container, WSL without
// gnome-keyring, server distros, CI runners). go-keyring's Set/Get
// calls then fail with a D-Bus error and the binary can't store the
// token at all. This file backs the keychain interface with an
// AES-256-GCM encrypted file at ~/.drift/.secrets when that happens,
// so the install + relay path still work on headless Linux.
//
// Threat model: the file is mode 0600 in the user's home directory. An
// attacker with read access to that file ALSO has read access to
// /etc/machine-id on the same box (or the user's home, where they could
// just read the cleartext file directly). Encryption here is defense
// against casual snooping (backup tarballs, accidental git adds,
// /tmp/restore-from-snapshot mistakes), not against a local attacker
// with full filesystem read. The KDF derives the AES key from
// /etc/machine-id via HKDF-SHA256, so copying the .secrets file to
// another machine yields garbage on decrypt -- the file is bound to
// the host.
//
// macOS + Windows do not exercise this path. The package-level Set/Get/
// Delete consult fallbackBackend() which returns nil on non-Linux.

package keychain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/hkdf"

	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/log"
)

// secretsFileName is the basename of the encrypted fallback store. The
// leading dot keeps it out of casual `ls` output; the actual security
// comes from the 0600 mode + encryption, not the filename.
const secretsFileName = ".secrets"

// machineIDPaths is the lookup order for the per-host KDF input. systemd
// writes /etc/machine-id on first boot; D-Bus writes the legacy path on
// systems that predate systemd. Trying both keeps us working on Alpine
// + Devuan + the occasional WSL distro that doesn't have systemd.
var machineIDPaths = []string{
	"/etc/machine-id",
	"/var/lib/dbus/machine-id",
}

// hkdfInfo is the static "info" string passed into HKDF-SHA256 so the
// derived key is domain-separated from any other use of the machine ID.
// Bumping this string invalidates every existing .secrets file (forces
// re-login); leave alone unless rotating.
const hkdfInfo = "drift-cli/keychain-fallback/v1"

// secretsBlob is the on-disk shape inside the encrypted file. Keyed by
// the same constants the keyring path uses (token / install_id /
// ecdh_privkey_hex) so callers never see the difference.
type secretsBlob struct {
	Items map[string]string `json:"items"`
}

// fallback owns the encrypted-file-backed keystore. Cached behind a
// sync.Once so the AES key derivation runs at most once per process.
// The file is read on every Get and rewritten on every Set; concurrent
// access from a single drift process is mediated by the package-level
// mutex.
type fallbackStore struct {
	path string
	key  []byte
	mu   sync.Mutex
}

var (
	fallbackInit sync.Once
	fallbackInst *fallbackStore
	fallbackErr  error
)

// fallbackBackend returns the lazily-initialized fallback store, or nil
// when this platform/host doesn't need (or can't build) one. Callers
// that get nil should treat the keyring as the only backend.
func fallbackBackend() *fallbackStore {
	if runtime.GOOS != "linux" {
		return nil
	}
	fallbackInit.Do(func() {
		home, err := config.Home()
		if err != nil {
			fallbackErr = err
			return
		}
		key, err := deriveFallbackKey()
		if err != nil {
			fallbackErr = err
			return
		}
		fallbackInst = &fallbackStore{
			path: filepath.Join(home, ".drift", secretsFileName),
			key:  key,
		}
	})
	if fallbackErr != nil {
		return nil
	}
	return fallbackInst
}

// deriveFallbackKey runs HKDF-SHA256 over /etc/machine-id (or the dbus
// legacy path) to produce a 32-byte AES-256 key. Returns an error when
// neither path exists; the caller treats that as "no fallback possible
// on this host" and surfaces the original keyring error.
func deriveFallbackKey() ([]byte, error) {
	machineID, err := readMachineID()
	if err != nil {
		return nil, err
	}
	if len(machineID) == 0 {
		return nil, errors.New("machine-id is empty")
	}
	r := hkdf.New(sha256.New, machineID, nil, []byte(hkdfInfo))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("derive fallback key: %w", err)
	}
	return key, nil
}

func readMachineID() ([]byte, error) {
	var lastErr error
	for _, p := range machineIDPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			return []byte(strings.TrimSpace(string(data))), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return nil, fmt.Errorf("no machine-id available: %w", lastErr)
}

// load reads the encrypted file and returns the decoded blob. Returns
// an empty blob (not an error) when the file doesn't exist; that's the
// fresh-install case before any Set has run.
func (s *fallbackStore) load() (*secretsBlob, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &secretsBlob{Items: map[string]string{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	plain, err := s.decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", s.path, err)
	}
	var blob secretsBlob
	if err := json.Unmarshal(plain, &blob); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if blob.Items == nil {
		blob.Items = map[string]string{}
	}
	return &blob, nil
}

// save encrypts and writes the blob atomically.
func (s *fallbackStore) save(blob *secretsBlob) error {
	plain, err := json.Marshal(blob)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(plain)
	if err != nil {
		return err
	}
	return config.AtomicWriteFile(s.path, enc, 0o600)
}

// encrypt seals plaintext with AES-256-GCM. Output is nonce || ciphertext.
// Nonce is freshly random per write; reusing one would compromise GCM,
// and we have no reason to be deterministic here.
func (s *fallbackStore) encrypt(plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// decrypt opens a sealed blob in the same format encrypt produced.
// Returns an error on tampering, host change (machine-id rotated), or
// truncation. Caller treats decryption failure the same as a missing
// file from the user's POV: the customer has to re-login.
func (s *fallbackStore) decrypt(data []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize+gcm.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}

func (s *fallbackStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := s.load()
	if err != nil {
		return err
	}
	blob.Items[key] = value
	return s.save(blob)
}

func (s *fallbackStore) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := s.load()
	if err != nil {
		return "", err
	}
	v, ok := blob.Items[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *fallbackStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := blob.Items[key]; !ok {
		return nil
	}
	delete(blob.Items, key)
	return s.save(blob)
}

// isKeyringUnavailable matches the error strings go-keyring surfaces
// when there's no Secret Service to talk to. Substring match because
// the upstream library wraps D-Bus errors as plain strings without
// exporting sentinels we can errors.Is against. Conservative on what
// counts as "unavailable": anything that mentions the service name,
// dbus, or the well-known D-Bus address path triggers the fallback.
func isKeyringUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "secretservice"),
		strings.Contains(msg, "secret service"),
		strings.Contains(msg, "org.freedesktop.secrets"),
		strings.Contains(msg, "org.freedesktop.dbus"),
		strings.Contains(msg, "the name is not activatable"),
		strings.Contains(msg, "dbus-launch"),
		strings.Contains(msg, "session bus"),
		strings.Contains(msg, "dbus_session_bus_address"),
		strings.Contains(msg, "no such file or directory") && strings.Contains(msg, "dbus"):
		return true
	}
	return false
}

// warnFallbackOnce emits one stderr line + one structured log line on
// first activation of the fallback for this process. The cli/install
// + cli/login flows print human-readable next-steps separately; this
// is the in-keychain-package signal so `drift status` / future doctor
// runs surface "you are on the encrypted file fallback".
var warnFallbackOnce sync.Once

func warnFallback() {
	warnFallbackOnce.Do(func() {
		fmt.Fprintln(os.Stderr,
			"drift: Linux Secret Service unavailable; storing credentials in ~/.drift/.secrets (encrypted, mode 0600). "+
				"This is normal on headless servers and SSH sessions without gnome-keyring.")
		log.Info("keychain", "fallback_engaged", map[string]any{
			"reason": "secret_service_unavailable",
		})
	})
}
