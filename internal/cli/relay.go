package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	driftlog "github.com/SHC-Labs/drift/internal/log"
	"github.com/SHC-Labs/drift/internal/ipc"
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
