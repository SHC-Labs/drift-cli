// Package cli wires the cobra command tree. Subcommand handlers live in
// sibling files: install.go, uninstall.go, init.go, etc. Hidden commands
// (drift internal hook *, drift _service) are registered here too but
// suppressed from --help via cobra's Hidden flag.
package cli

import (
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "drift",
		Short: "Drift coordination CLI + relay",
		Long: `Drift is a coordination layer for AI agents working alongside humans.
This binary runs locally as a service, proxies MCP traffic to the Drift
server, fires hooks on every prompt, and keeps your team's activity feed
in sync.

See https://drift.io for docs.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newVersionCmd(),
		newInstallCmd(),
		newUninstallCmd(),
		newInitCmd(),
		newUninitCmd(),
		newQuickstartCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newTokenCmd(),
		newStatusCmd(),
		newDoctorCmd(),
		newRelayCmd(),
		newUpdateCmd(),
		newTelemetryCmd(),
		newInternalCmd(),
		newServiceCmd(),
	)

	return root
}

// Execute runs the root command. main() calls this and exits non-zero on err.
func Execute() error {
	return newRootCmd().Execute()
}
