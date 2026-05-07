//go:build !windows

package service

import "errors"

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
