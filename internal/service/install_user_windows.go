//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	driftlog "github.com/SHC-Labs/drift/internal/log"
)

// CreateNoWindow is the Windows process creation flag that runs a
// process without giving it a console. Different from DETACHED_PROCESS:
// DETACHED_PROCESS leaves the child with no inherited console handles
// at all, which the Go runtime can mishandle (intermittent invalid-
// handle panics during runtime init), and makes the child's stdio
// reads/writes return ERROR_INVALID_HANDLE the moment anything in the
// runtime or imported packages touches them. CREATE_NO_WINDOW gives
// the child a real (hidden) console it can write to without crashing.
//
// We pair this with explicit Stdin/Stdout/Stderr redirection (NUL for
// stdin, the drift log file for stdout+stderr) so any panic from the
// child still ends up in a readable file post-mortem instead of being
// swallowed by a closed handle. v0.1.18 - v0.1.21 used
// DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP and the child died
// silently within seconds of launch; CREATE_NO_WINDOW + explicit
// redirection is the proven pattern (Tailscale + Syncthing both ship
// it for their non-admin Windows daemon paths).
const createNoWindow = 0x08000000

// InstallUserMode is the Windows fallback for when kardianos service
// install or start fails because PowerShell isn't running as admin.
// Drops a .cmd launcher in the user's Startup folder so the relay
// starts on next login, and launches the relay process now so the
// customer doesn't have to log out + back in.
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

	logPath := driftlog.LogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", fmt.Errorf("create log dir %s: %w", filepath.Dir(logPath), err)
	}

	cmdPath := filepath.Join(startup, "drift-relay.cmd")
	// v0.1.22: launch via `cmd /C start /B` with explicit stdout/stderr
	// redirect. Plain `start "" /B exe args` inherits cmd.exe's console
	// for the child; when cmd.exe exits at the end of the .cmd, the
	// console handles get invalidated and the next stdio write from the
	// child crashes it. Wrapping in `cmd /C` with `> log 2>&1` gives
	// the child stdio that points at a real file the OS keeps valid for
	// the lifetime of the process.
	content := fmt.Sprintf(
		"@echo off\r\n"+
			"start \"\" /B cmd /C \"\"%s\" _relay >> \"%s\" 2>&1\"\r\n",
		exe, logPath,
	)
	if err := os.WriteFile(cmdPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", cmdPath, err)
	}

	// Launch the relay NOW so the customer doesn't have to log out
	// + back in. CREATE_NO_WINDOW gives the child a hidden console
	// with valid stdio handles; explicit Stdin=NUL + Stdout/Stderr=log
	// file ensures the child has somewhere to write that survives the
	// parent install process exiting.
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return cmdPath, fmt.Errorf("autostart installed at %s but log open failed: %w", cmdPath, err)
	}
	defer logFile.Close()

	nul, err := os.OpenFile("NUL", os.O_RDWR, 0)
	if err != nil {
		return cmdPath, fmt.Errorf("autostart installed at %s but NUL open failed: %w", cmdPath, err)
	}
	defer nul.Close()

	cmd := exec.Command(exe, "_relay")
	cmd.Stdin = nul
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return cmdPath, fmt.Errorf("autostart installed at %s but immediate launch failed: %w", cmdPath, err)
	}
	// Don't wait — leave it running. The Process.Release call lets the
	// OS reap the child without us holding a Wait goroutine.
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
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

// IsAlreadyExists returns true when err looks like the OS service
// manager rejecting an install because the service is already
// registered. Hits two paths that produce different strings:
// kardianos's own check for an existing systemd unit / launchd plist
// returns "Init already exists"; Windows' SCM CreateService syscall
// returns "service already exists" (ERROR_SERVICE_EXISTS, code 1073).
// String-match for the same reason as IsAccessDenied: kardianos wraps
// without exporting a sentinel.
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "error_service_exists")
}
