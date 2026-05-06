//go:build windows

package ipc

import (
	"syscall"
)

// soExclusiveAddrUse is the Windows socket option that prevents another
// process from binding the same address even with SO_REUSEADDR. Without
// this, Windows lets a second process steal the port via SO_REUSEADDR
// shenanigans, which is the localhost-port-hijack threat documented in
// THREAT_MODEL.md.
//
// Constant value -5 per ws2def.h. Hardcoded to avoid pulling in
// golang.org/x/sys/windows just for one constant.
const soExclusiveAddrUse = -5

// hardenedControl sets SO_EXCLUSIVEADDRUSE on the listening socket
// before bind. Called from net.ListenConfig.Control via BindHardened.
func hardenedControl(network, address string, c syscall.RawConn) error {
	var setErr error
	err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
	})
	if err != nil {
		return err
	}
	return setErr
}
