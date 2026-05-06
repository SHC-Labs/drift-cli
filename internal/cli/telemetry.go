package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/telemetry"
)

func newTelemetryCmd() *cobra.Command {
	tel := &cobra.Command{
		Use:   "telemetry",
		Short: "Opt into or out of telemetry",
		Long: `Telemetry sends only the four install state events (cli-installed,
client-connected, relay-enabled, relay-heartbeat) plus version + OS-arch.
Never file paths, project names, code, or prompts. Set DRIFT_NO_TELEMETRY=1
in your environment for the same effect (per-process; this subcommand
sets the persistent preference).

See PRIVACY.md for the full collection list and retention policy.`,
	}
	tel.AddCommand(
		&cobra.Command{
			Use:   "on",
			Short: "Opt into telemetry",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := telemetry.SetEnabled(true); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Telemetry: on")
				return nil
			},
		},
		&cobra.Command{
			Use:   "off",
			Short: "Opt out of telemetry",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := telemetry.SetEnabled(false); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Telemetry: off")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show current telemetry state",
			RunE: func(cmd *cobra.Command, args []string) error {
				state := "off"
				if telemetry.Enabled() {
					state = "on"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Telemetry: %s\n", state)
				return nil
			},
		},
	)
	return tel
}
