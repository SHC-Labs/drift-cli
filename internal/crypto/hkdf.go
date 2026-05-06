package crypto

import (
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// HKDFSHA256 is the key derivation function the TS relay uses for every
// derived key (KEK wrap, session key, tag key). Empty salt, info varies
// per derivation site, output length 32 bytes for symmetric keys.
//
// Mirrors Node's hkdfSync('sha256', ikm, salt, info, length) byte-for-byte.
func HKDFSHA256(ikm, salt, info []byte, length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("hkdf: length must be positive, got %d", length)
	}
	r := hkdf.New(sha256.New, ikm, salt, info)
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("hkdf: read: %w", err)
	}
	return out, nil
}

// Fixed HKDF info strings, byte-identical to the TS relay's constants.
// CHANGING ANY OF THESE BREAKS WIRE COMPAT. They're locked in for v1
// crypto and would need a crypto algorithm version bump to change.
const (
	// InfoKEKWrap is the info parameter for ECDH-derived symmetric keys
	// used to wrap KEKs for recipients. From ecdh.ts HKDF_INFO.
	InfoKEKWrap = "drift-kek-wrap-v1"

	// InfoSessionPrefix is prepended to today's UTC date (YYYY-MM-DD)
	// to form the per-day session key derivation info. From key-derive.ts
	// SESSION_INFO_PREFIX.
	InfoSessionPrefix = "drift-session:"

	// InfoTagKey is the info parameter for the per-org tag-hashing key
	// used by the search-token HMAC chain. From key-derive.ts TAG_INFO.
	InfoTagKey = "drift-tag-v1"

	// FingerprintMessage is the HMAC-SHA256 message used to compute the
	// 4-byte fingerprint of an org key. From key-derive.ts inline literal.
	FingerprintMessage = "drift-relay:fingerprint"
)
