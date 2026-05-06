package config

import (
	"errors"
	"fmt"
	"strings"
)

// Token version discriminator. v1 tokens look like drift_v1_<payload>;
// legacy tokens that pre-date the binary are drift_<hex> with no
// version prefix and get treated as implicit v1 for back-compat.
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
// missing prefix is rejected. Legacy drift_<hex> with at least 16 hex
// chars is accepted as implicit v1.
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
		if !isHexLike(payload) {
			return "", fmt.Errorf("%w: v1 payload must be hex-like (a-f, 0-9), got %q", ErrTokenFormat, payload)
		}
		return "v1", nil
	}

	// Reject anything that LOOKS versioned but isn't recognized.
	// drift_v2_..., drift_v9_..., drift_vX_... all fail strictly.
	if looksVersioned(rest) {
		return "", fmt.Errorf("%w: unknown version prefix in %q (this binary handles v1 only)", ErrTokenFormat, token)
	}

	// Legacy drift_<hex>: pre-version-discriminator format. Accept if
	// the payload is plausibly hex and at least 16 chars.
	if !isHexLike(rest) {
		return "", fmt.Errorf("%w: legacy payload must be hex-like, got %q", ErrTokenFormat, rest)
	}
	if len(rest) < 16 {
		return "", fmt.Errorf("%w: legacy token too short (%d chars, want 16+)", ErrTokenFormat, len(rest))
	}
	return "legacy", nil
}

// isHexLike returns true if s contains only [0-9a-fA-F]. We don't care
// about exact length here; the caller enforces minimums.
func isHexLike(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// looksVersioned returns true if the post-prefix string starts with
// "vN_" where N is one or more digits. Used to reject unknown versions
// loud.
func looksVersioned(s string) bool {
	if !strings.HasPrefix(s, "v") {
		return false
	}
	rest := s[1:]
	// Walk digits, then expect '_'.
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	return i < len(rest) && rest[i] == '_'
}
