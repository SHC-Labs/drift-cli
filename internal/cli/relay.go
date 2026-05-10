package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/ipc"
	driftlog "github.com/SHC-Labs/drift/internal/log"
	"github.com/SHC-Labs/drift/internal/relay"
	"github.com/SHC-Labs/drift/internal/service"
)

func newRelayCmd() *cobra.Command {
	relay := &cobra.Command{
		Use:   "relay",
		Short: "Inspect the embedded relay",
		Long:  "The relay runs as a goroutine inside the service. Customers don't manage it directly.",
	}
	var logLines int
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Tail recent relay logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRelayLogs(cmd.OutOrStdout(), logLines)
		},
	}
	logsCmd.Flags().IntVarP(&logLines, "lines", "n", 100, "Number of lines to print")
	relay.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Embedded relay state",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runRelayStatus(cmd.OutOrStdout())
			},
		},
		logsCmd,
	)
	return relay
}

func runRelayLogs(stdout io.Writer, n int) error {
	if n <= 0 {
		n = 100
	}
	lines, err := driftlog.Tail(n)
	if err != nil {
		return fmt.Errorf("read log: %w", err)
	}
	if len(lines) == 0 {
		fmt.Fprintln(stdout, "(no log lines yet)")
		return nil
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// newRelayDaemonCmd is the v0.1.20 add. Hidden _relay subcommand that
// runs the relay daemon directly, no kardianos in the path. Replaces
// drift.exe _service as the entrypoint InstallUserMode launches on
// Windows non-admin: kardianos's interactive-mode fallback bails or
// dies silently in detached / no-console contexts, which left every
// non-admin Windows install with a registered Startup .cmd that
// couldn't actually keep the relay alive (relay starts at login,
// dies a few minutes later, no autostart). _relay just calls
// relay.Run directly and blocks on signals — same lifecycle the SCM
// path uses internally, just without the SCM dispatcher in front.
//
// Hidden because customers shouldn't call it directly. The launcher
// .cmd dropped by InstallUserMode runs it; nothing else should.
func newRelayDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_relay",
		Hidden: true,
		Short:  "Run the relay daemon in-process (used by InstallUserMode launchers, not for human use).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRelayDaemon()
		},
	}
}

func runRelayDaemon() error {
	port, err := ipc.CurrentPort()
	if err != nil {
		return fmt.Errorf("read relay port: %w", err)
	}
	if port == 0 {
		return errors.New("no relay port persisted; run 'drift install' first")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	l, err := ipc.BindHardened(ctx, port)
	if err != nil {
		return fmt.Errorf("bind relay port %d: %w", port, err)
	}

	upstream := relay.DefaultUpstream
	if v := os.Getenv("DRIFT_API_URL"); v != "" {
		upstream = v
	}

	go relay.FireRelayEnabled(ctx, upstream, "http", strconv.Itoa(port))
	go relay.RunHeartbeat(ctx, upstream)

	return relay.Run(ctx, l, relay.Options{Upstream: upstream})
}

func runRelayStatus(stdout io.Writer) error {
	port, err := ipc.CurrentPort()
	if err != nil {
		return fmt.Errorf("read relay port: %w", err)
	}
	svc, _ := service.Status()
	healthy := false
	if port > 0 {
		healthy = probeRelayHealth(port)
	}

	fmt.Fprintln(stdout, "relay")
	if port > 0 {
		fmt.Fprintf(stdout, "  port:    127.0.0.1:%d\n", port)
	} else {
		fmt.Fprintln(stdout, "  port:    not set (run 'drift install')")
	}
	fmt.Fprintf(stdout, "  service: %s\n", svc)
	fmt.Fprintf(stdout, "  health:  %s\n", boolStr(healthy, "up", "down"))
	if !healthy && port > 0 {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Relay is not responding on its persisted port. Things to check:")
		fmt.Fprintln(stdout, "  - is the service running? (drift status)")
		fmt.Fprintln(stdout, "  - is another process bound to the port? ('lsof -i :"+
			fmt.Sprintf("%d", port)+"' on macOS / Linux, 'netstat -ano' on Windows)")
	}
	return nil
}
