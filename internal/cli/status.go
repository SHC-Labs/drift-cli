package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/doctor"
	"github.com/SHC-Labs/drift/internal/ipc"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/service"
)

// statusProbeTimeout is the budget for the local relay /health probe.
// Short because we're hitting localhost; anything slower than this is
// an unhealthy relay.
const statusProbeTimeout = 1 * time.Second

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Brief health check",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd.OutOrStdout())
		},
	}
}

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	var logLines int
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Verbose diagnostics dump for support tickets",
		Long: `Prints binary version, server reachability, token validity, project status,
per-client hook health, service status, last 50 log lines. Pipe the output
to hello@driftlabs.io and most support tickets resolve themselves.

--json emits the same data as a structured JSON document for automated
collection (CI, support form, etc).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := doctor.Run(cmd.Context(), logLines)
			doctor.Write(cmd.OutOrStdout(), report, asJSON)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&logLines, "log-lines", 50, "Number of recent log lines to include (0 to disable)")
	return cmd
}

func runStatus(stdout io.Writer) error {
	port, portErr := ipc.CurrentPort()
	tokenPresent := false
	if v, err := keychain.GetToken(); err == nil && v != "" {
		tokenPresent = true
	}
	installID, _ := keychain.GetInstallID()

	svcState, _ := service.Status()

	// /health probe: is the relay actually answering?
	relayHealthy := false
	if port > 0 {
		relayHealthy = probeRelayHealth(port)
	}

	// mcpStatus tracks three states: present-and-valid, missing, corrupt.
	// "present" + "missing" had been the only options; corrupt looked
	// like missing, which sent customers down the wrong fix path.
	var mcpStatus string
	switch _, err := config.ReadMCP(); {
	case err == nil:
		mcpStatus = "present"
	case errors.Is(err, config.ErrMCPMissing):
		mcpStatus = "missing"
	case errors.Is(err, config.ErrMCPCorrupt):
		mcpStatus = "corrupt at " + config.MCPPath() + " (run 'drift install' to repair)"
	default:
		// Drift entry missing, token missing, etc. The file itself is
		// fine; the contents are incomplete. Show the underlying reason.
		mcpStatus = "incomplete (" + err.Error() + ")"
	}

	fmt.Fprintln(stdout, "drift status")
	fmt.Fprintf(stdout, "  service:       %s\n", svcState)
	switch {
	case errors.Is(portErr, config.ErrConfigVersionFuture):
		fmt.Fprintf(stdout, "  relay port:    config schema is newer than this binary supports — upgrade drift (%s)\n", config.BinaryConfigPath())
	case errors.Is(portErr, config.ErrConfigCorrupt):
		fmt.Fprintf(stdout, "  relay port:    config corrupt at %s (run 'drift install' to repair)\n", config.BinaryConfigPath())
	case port > 0:
		fmt.Fprintf(stdout, "  relay port:    %d\n", port)
		fmt.Fprintf(stdout, "  relay health:  %s\n", boolStr(relayHealthy, "up", "down"))
	default:
		fmt.Fprintln(stdout, "  relay port:    not set (run 'drift install')")
	}
	fmt.Fprintf(stdout, "  ~/.mcp.json:   %s\n", mcpStatus)
	fmt.Fprintf(stdout, "  token:         %s\n", boolStr(tokenPresent, "in keychain", "missing (run 'drift login' or DRIFT_TOKEN= drift install)"))
	if installID != "" {
		fmt.Fprintf(stdout, "  install_id:    %s\n", installID)
	}
	return nil
}

// probeRelayHealth hits 127.0.0.1:<port>/health with a short timeout.
// Returns true on any 2xx response. Used by drift status + drift relay
// status; cheap because it's a local request.
func probeRelayHealth(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), statusProbeTimeout)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
