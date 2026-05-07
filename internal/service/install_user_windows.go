//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// InstallUserMode is the Windows fallback for when kardianos service
// install fails because PowerShell isn't running as admin. Drops a
// .cmd launcher in the user's Startup folder so the relay starts on
// next login, and launches the relay process now so the customer
// doesn't have to log out + back in.
//
// Less robust than a real Windows Service: no auto-restart on crash,
// no system-wide persistence, no stdout/stderr capture into Event Log.
// Customers who care can re-run drift install with PowerShell elevated
// to upgrade to a real Service.
//
// Returns the launcher path (for the install message) and any error.
func InstallUserMode() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA env var not set; cannot locate Startup folder")
	}
	startup := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	if err := os.MkdirAll(startup, 0o755); err != nil {
		return "", fmt.Errorf("create Startup folder: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate drift executable: %w", err)
	}
	cmdPath := filepath.Join(startup, "drift-relay.cmd")
	// `start "" /b` launches detached, no console window. The empty
	// "" is the title argument start.exe requires when the first arg
	// would otherwise be parsed as the title.
	content := fmt.Sprintf("@echo off\r\nstart \"\" /b \"%s\" _service\r\n", exe)
	if err := os.WriteFile(cmdPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", cmdPath, err)
	}
	// Launch the service NOW so the customer doesn't have to log out
	// + back in. Detached (CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS)
	// so it survives the parent install process exiting.
	cmd := exec.Command(exe, "_service")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		return cmdPath, fmt.Errorf("autostart installed at %s but immediate launch failed: %w", cmdPath, err)
	}
	// Don't wait — leave it running.
	return cmdPath, nil
}

// IsAccessDenied returns true when err looks like the Windows
// "Access is denied" error kardianos returns when service install
// needs elevation it doesn't have. String-match is the only reliable
// way: kardianos wraps the underlying syscall error in its own type
// without exporting a sentinel.
func IsAccessDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Access is denied") ||
		strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "ERROR_ACCESS_DENIED")
}
