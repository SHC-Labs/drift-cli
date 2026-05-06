package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SessionKeyBytes is the size of a derived session key in bytes.
const SessionKeyBytes = 32

// TagKeyBytes is the size of the per-org tag-hashing key in bytes.
const TagKeyBytes = 32

// FingerprintBytes is the truncated HMAC output used for org-key fingerprints.
const FingerprintBytes = 4

// DeriveSessionKey returns the per-day session key derived from the org
// key. Mirrors key-derive.ts deriveSessionKey:
//
//	hkdfSync('sha256', orgKey, '', 'drift-session:' + dateStr, 32)
//
// dateStr is the UTC date in YYYY-MM-DD format. Pass an explicit time
// to control date boundaries in tests.
func DeriveSessionKey(orgKey []byte, t time.Time) ([]byte, error) {
	dateStr := t.UTC().Format("2006-01-02")
	info := []byte(InfoSessionPrefix + dateStr)
	return HKDFSHA256(orgKey, nil, info, SessionKeyBytes)
}

// DeriveTagKey returns the per-org tag-hashing key derived from the org
// DEK. Mirrors key-derive.ts deriveTagKey:
//
//	hkdfSync('sha256', orgDek, '', 'drift-tag-v1', 32)
func DeriveTagKey(orgDek []byte) ([]byte, error) {
	return HKDFSHA256(orgDek, nil, []byte(InfoTagKey), TagKeyBytes)
}

// Fingerprint returns the 4-byte hex fingerprint of an org key. Mirrors
// key-derive.ts fingerprint:
//
//	HMAC-SHA256(orgKey, 'drift-relay:fingerprint')[0:4] as hex
func Fingerprint(orgKey []byte) string {
	mac := hmac.New(sha256.New, orgKey)
	mac.Write([]byte(FingerprintMessage))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:FingerprintBytes])
}

// TodayUTC returns today's date as YYYY-MM-DD in UTC. Mirrors
// key-derive.ts todayUtc(). Used by the relay's per-day key rotation.
func TodayUTC(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}

// EnsureKeyLen returns key if it has the expected length, otherwise an
// error. Crypto callers use this to validate inputs before dispatching
// to AES / ECDH / HKDF.
func EnsureKeyLen(key []byte, expected int, name string) error {
	if len(key) != expected {
		return fmt.Errorf("%s: must be %d bytes, got %d", name, expected, len(key))
	}
	return nil
}
