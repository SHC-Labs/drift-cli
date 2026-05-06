package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// osHostname wraps os.Hostname so callers in this package don't need
// the os import for that one call. Returns empty string + nil on a
// platform where Hostname returns an empty value (rare).
func osHostname() (string, error) {
	return os.Hostname()
}

// shortHash returns SHA-256 of s as hex, truncated to 16 chars (64
// bits of entropy). More than enough to distinguish multi-machine
// installs by the same developer; not enough to fingerprint a
// specific host. Privacy-preserving by design per PRIVACY.md.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}
