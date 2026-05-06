//go:build !windows

package ipc

import (
	"syscall"
)

// hardenedControl is a no-op on Linux/macOS/BSD: the kernel default
// behavior already prevents two processes from binding the same TCP port
// simultaneously without SO_REUSEPORT being explicitly set on both
// sockets. We don't set SO_REUSEPORT, so we get the safe default.
//
// Mirrors the Windows version's signature so BindHardened can call the
// same function name regardless of platform.
func hardenedControl(network, address string, c syscall.RawConn) error {
	return nil
}
