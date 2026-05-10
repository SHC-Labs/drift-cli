//go:build !windows

package service

import (
	"errors"
	"strings"
)

// InstallUserMode is a no-op stub for non-Windows platforms. The
// admin-vs-user-mode distinction only applies on Windows; Linux and
// macOS use systemd user units and launchd plists that don't need
// elevation.
func InstallUserMode() (string, error) {
	return "", errors.New("user-mode autostart fallback is Windows-only")
}

// IsAccessDenied is always false off Windows.
func IsAccessDenied(error) bool {
	return false
}

// IsAlreadyExists is shared with the Windows path: kardianos surfaces
// the same "already exists" string on systemd / launchd when the unit
// file is present from a previous install, so the same string-match
// works cross-platform. See the Windows file for the rationale.
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
