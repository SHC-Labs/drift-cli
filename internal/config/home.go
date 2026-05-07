package config

import (
	"errors"
	"os"
)

// ErrHomeUnset is returned by callers that need a home directory and
// can't get one. Without HOME, drift can't safely locate ~/.mcp.json,
// ~/.drift/config.json, or the keychain backing files. Falling back to
// a CWD-relative read is dangerous: a customer running drift status
// inside a hostile project directory would otherwise be served the
// project's `.mcp.json` as if it were their own.
var ErrHomeUnset = errors.New("HOME not set; drift requires a home directory to locate configs")

// Home returns the resolved home directory or ErrHomeUnset. Wraps
// os.UserHomeDir but never returns "" without an error, so callers
// don't accidentally build relative paths from an empty string.
func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ErrHomeUnset
	}
	if home == "" {
		return "", ErrHomeUnset
	}
	return home, nil
}
