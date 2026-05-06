// Package service wraps github.com/kardianos/service so a single Go API
// installs the drift binary as a systemd user unit (Linux), launchd
// plist (macOS), or Windows Service (Windows). All user-scope; no
// elevation needed.
//
// drift install calls Install + Start. drift uninstall calls Stop +
// Uninstall. drift _service is the entrypoint the OS service manager
// invokes at boot to run the relay daemon.
package service

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/kardianos/service"

	"github.com/SHC-Labs/drift/internal/ipc"
	"github.com/SHC-Labs/drift/internal/relay"
)

// Name is the service identifier the OS uses to track it. Stable
// across versions; renaming would orphan existing installs.
const Name = "drift"

// DisplayName is what the user sees in service managers (Activity
// Monitor, services.msc, systemctl status).
const DisplayName = "Drift"

// Description shows in service-list views. Short, customer-facing.
const Description = "Drift coordination relay (https://drift.io)"

// program implements service.Interface so kardianos can lifecycle it.
// Start launches the relay in a goroutine and returns immediately; Stop
// cancels the context to wind it down.
type program struct {
	cancel   context.CancelFunc
	listener net.Listener
}

// Start is called by the OS service manager. Must return promptly:
// the SCM has a strict deadline (~30s on Windows) and a slow Start
// gets the service marked as failed.
func (p *program) Start(s service.Service) error {
	port, err := ipc.CurrentPort()
	if err != nil {
		return fmt.Errorf("read relay port: %w", err)
	}
	if port == 0 {
		return fmt.Errorf("no relay port persisted; run 'drift install' first")
	}

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	l, err := ipc.BindHardened(ctx, port)
	if err != nil {
		cancel()
		return fmt.Errorf("bind relay port %d: %w", port, err)
	}
	p.listener = l

	upstream := relay.DefaultUpstream
	if v := os.Getenv("DRIFT_API_URL"); v != "" {
		upstream = v
	}

	go func() {
		// relay.Run owns the listener for ctx's lifetime. Errors are
		// logged via the service framework's logger; we don't return
		// them from Start because the SCM has already moved past us.
		if err := relay.Run(ctx, l, relay.Options{Upstream: upstream}); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "drift relay: %v\n", err)
		}
	}()

	// Fire relay-enabled state event + start heartbeat goroutine.
	// Both are no-ops without a keychain token/install_id; they don't
	// block service startup.
	go relay.FireRelayEnabled(ctx, upstream, "http", fmt.Sprintf("%d", port))
	go relay.RunHeartbeat(ctx, upstream)

	return nil
}

// Stop is called by the OS service manager on shutdown / restart. We
// cancel the context (signaling relay.Run to drain) and let the
// goroutine exit naturally. Listener close happens inside relay.Run
// via http.Server.Shutdown.
func (p *program) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

// New returns a kardianos service handle for drift. Install / Start /
// Stop / Uninstall methods on the returned value drive the OS service
// manager.
func New() (service.Service, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate drift binary: %w", err)
	}
	cfg := &service.Config{
		Name:        Name,
		DisplayName: DisplayName,
		Description: Description,
		Executable:  exePath,
		// _service is the hidden cobra subcommand the SCM invokes.
		Arguments: []string{"_service"},
		// User-scope on macOS + Linux; no elevation prompt.
		Option: service.KeyValue{
			"UserService": true,
			// Restart on non-zero exit so the relay self-heals from
			// transient panics. kardianos translates this to systemd
			// Restart=on-failure / launchd KeepAlive=true / Windows
			// SC_ACTION_RESTART.
			"Restart": "on-failure",
		},
	}
	return service.New(&program{}, cfg)
}

// Install registers the service with the OS service manager. Creates
// the systemd unit / launchd plist / Windows Service entry. Idempotent:
// re-running drift install just upserts the entry.
func Install() error {
	s, err := New()
	if err != nil {
		return err
	}
	if err := s.Install(); err != nil {
		// kardianos returns a "service already exists" error which we
		// treat as success (idempotent install).
		return fmt.Errorf("install %s service: %w", Name, err)
	}
	return nil
}

// Uninstall removes the service from the OS service manager.
// Idempotent: removing a non-existent service is not an error.
func Uninstall() error {
	s, err := New()
	if err != nil {
		return err
	}
	if err := s.Uninstall(); err != nil {
		return fmt.Errorf("uninstall %s service: %w", Name, err)
	}
	return nil
}

// Start tells the OS service manager to run the service. Returns
// quickly; the actual relay startup happens asynchronously inside the
// service process.
func Start() error {
	s, err := New()
	if err != nil {
		return err
	}
	return s.Start()
}

// Stop tells the OS service manager to wind down the service.
func Stop() error {
	s, err := New()
	if err != nil {
		return err
	}
	return s.Stop()
}

// Run is the service-process entrypoint. drift _service calls this
// after kardianos invokes the binary. Blocks until the OS service
// manager signals shutdown.
func Run() error {
	s, err := New()
	if err != nil {
		return err
	}
	return s.Run()
}

// Status returns a string describing the service state ("running",
// "stopped", "unknown"). Used by drift status + drift doctor.
func Status() (string, error) {
	s, err := New()
	if err != nil {
		return "unknown", err
	}
	st, err := s.Status()
	if err != nil {
		return "unknown", err
	}
	switch st {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "stopped", nil
	default:
		return "unknown", nil
	}
}
