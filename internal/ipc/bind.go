package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// LocalBindAddr is the address the relay listens on. 127.0.0.1 only
// (loopback) so the relay never accepts traffic from outside the customer
// machine.
const LocalBindAddr = "127.0.0.1"

// probeTimeout is how long the startup probe waits before declaring the
// port free. Short because a real listener responds immediately.
const probeTimeout = 250 * time.Millisecond

// BindHardened binds 127.0.0.1:port for TCP with platform-specific
// hardening. On Windows it sets SO_EXCLUSIVEADDRUSE; elsewhere the
// kernel defaults already prevent two processes from binding the same
// port simultaneously.
//
// Performs a startup probe FIRST: attempt connect() to the port and bail
// loud if something already answers. This catches both leftover state
// from a crashed prior instance AND another process squatting on the
// port. The plan's policy is "refuse to bind alternate ports on conflict
// (don't try 47821 -> 47822 -> 47823, exit with clear error)" so we
// return a clean error instead of silently picking a different port.
//
// Sprint 2's relay code calls this. Sprint 1 ships the function so the
// bind shape is locked in.
func BindHardened(ctx context.Context, port int) (net.Listener, error) {
	addr := net.JoinHostPort(LocalBindAddr, strconv.Itoa(port))

	if err := probeForLeftover(ctx, addr); err != nil {
		return nil, err
	}

	lc := net.ListenConfig{Control: hardenedControl}
	l, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", addr, err)
	}
	return l, nil
}

// probeForLeftover attempts a TCP connect to addr. If it succeeds,
// something is already listening; fail loud. If it errors with
// connection-refused, the port is free. Any other error (timeout, host
// unreachable) we treat as "port is probably free" and let the bind
// itself fail loud if it isn't.
func probeForLeftover(ctx context.Context, addr string) error {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	d := net.Dialer{}
	conn, err := d.DialContext(probeCtx, "tcp", addr)
	if err == nil {
		_ = conn.Close()
		return ErrPortInUse{Addr: addr}
	}
	// Connection refused = port is free, exactly what we want.
	// Other errors (timeout, DNS, etc) we ignore here; the bind below
	// will surface any real problem with a clear message.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// On all platforms, "connection refused" comes through as a
		// net.OpError with a syscall error underneath. We don't
		// inspect the syscall errno explicitly because it varies; the
		// happy path for us is "any error on connect", and the bind
		// below catches the unhappy path.
		return nil
	}
	return nil
}

// ErrPortInUse is the loud-failure error when something already answers
// on the relay port. Includes the addr so doctor / status can format a
// useful diagnostic.
type ErrPortInUse struct {
	Addr string
}

func (e ErrPortInUse) Error() string {
	return fmt.Sprintf("relay port already in use at %s. Another process is listening; this could be a crashed prior drift instance or an unrelated service. Stop the other listener or run 'drift uninstall' followed by 'drift install' to pick a fresh port", e.Addr)
}
