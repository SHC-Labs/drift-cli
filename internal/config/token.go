package config

import (
	"errors"
	"fmt"
	"strings"
)

// Token version discriminator. The Drift dashboard issues
// drift_<base64url> today (48 random bytes encoded as base64url, 64
// chars after the prefix). Those tokens get treated as implicit v1 for
// back-compat. Tokens with an explicit drift_v1_<base64url> prefix are
// also accepted for forward-compat with a future server change.
//
// Future formats land at drift_v2_<payload> etc, validated strictly
// (drift_v2x_... is rejected, not silently treated as v1) so the parser
// can't be tricked into accepting a malformed token.
const (
	TokenPrefix    = "drift_"
	TokenVersionV1 = "v1_"
)

// ErrTokenFormat is the validation failure for malformed tokens.
// Surfaced to drift install + drift login when the user provides a
// token that doesn't match the expected shape.
var ErrTokenFormat = errors.New("token format invalid")

// ValidateToken checks that a token matches a known format. Returns
// the version ("v1" or "legacy") on success, or ErrTokenFormat with a
// detail reason on failure.
//
// Strict parsing: drift_v2x_... is rejected (looks v2-ish but isn't a
// valid version prefix), drift_v1_ with empty payload is rejected,
// missing prefix is rejected. drift_<base64url> with at least 16 chars
// is accepted as implicit v1.
func ValidateToken(token string) (version string, err error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return "", fmt.Errorf("%w: missing %q prefix", ErrTokenFormat, TokenPrefix)
	}
	rest := token[len(TokenPrefix):]
	if rest == "" {
		return "", fmt.Errorf("%w: empty after prefix", ErrTokenFormat)
	}

	// Try v1 explicitly first.
	if strings.HasPrefix(rest, TokenVersionV1) {
		payload := rest[len(TokenVersionV1):]
		if payload == "" {
			return "", fmt.Errorf("%w: empty payload after %sv1_", ErrTokenFormat, TokenPrefix)
		}
		if !isTokenPayload(payload) {
			return "", fmt.Errorf("%w: v1 payload must be base64url chars (A-Za-z0-9_-), got %d-char value (%s)", ErrTokenFormat, len(payload), redactToken(payload))
		}
		return "v1", nil
	}

	// Reject anything that LOOKS versioned but isn't recognized.
	// drift_v2_..., drift_v9_..., drift_vX_... all fail strictly. The
	// version prefix shape is what we report; the payload portion stays
	// redacted so error logs and support tickets don't leak the token.
	if looksVersioned(rest) {
		return "", fmt.Errorf("%w: unknown version prefix %s_ (this binary handles v1 only)", ErrTokenFormat, versionPrefix(rest))
	}

	// Implicit v1: drift_<base64url>. The dashboard generates these as
	// randomBytes(48).toString('base64url'), giving 64 chars over the
	// charset A-Za-z0-9_-. Accept if charset is right and length is at
	// least 16.
	if !isTokenPayload(rest) {
		return "", fmt.Errorf("%w: payload must be base64url chars (A-Za-z0-9_-), got %d-char value (%s)", ErrTokenFormat, len(rest), redactToken(rest))
	}
	if len(rest) < 16 {
		return "", fmt.Errorf("%w: token too short (%d chars, want 16+)", ErrTokenFormat, len(rest))
	}
	return "legacy", nil
}

// redactToken returns a short fingerprint of a token payload suitable
// for error messages, log lines, and support tickets. Echoes the first
// 4 and last 2 chars; replaces the middle with "...". Anything 8 chars
// or shorter is fully starred to avoid leaking enough to brute-force.
func redactToken(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + "..." + s[len(s)-2:]
}

// versionPrefix returns the leading "v<digits><alpha>" run from the
// post-prefix string up to but not including the first '_'. Used in the
// unknown-version error so the message names the prefix the user typed
// without leaking the random payload that follows. Falls back to a
// short fingerprint when no '_' is present.
func versionPrefix(rest string) string {
	if i := strings.Index(rest, "_"); i >= 0 {
		return rest[:i]
	}
	return redactToken(rest)
}

// isTokenPayload returns true if s contains only base64url chars
// [A-Za-z0-9_-]. The dashboard issues tokens as base64url-encoded
// random bytes; this matches that charset exactly.
func isTokenPayload(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// looksVersioned returns true if the post-prefix string starts with a
// "v<digits>" pattern followed (after optional letters and digits) by
// '_'. That covers canonical version forms like vN_ and mangled forms
// like v2x_, v2alpha_, v123beta_ that should reject as unknown versions
// rather than fall through to legacy parsing.
func looksVersioned(s string) bool {
	if !strings.HasPrefix(s, "v") {
		return false
	}
	rest := s[1:]
	// Need at least one digit immediately after 'v'.
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	// Walk any trailing letters and digits (mangled version suffixes
	// like "x", "alpha", "beta", "RC1").
	for i < len(rest) {
		r := rest[i]
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			i++
			continue
		}
		break
	}
	return i < len(rest) && rest[i] == '_'
}
