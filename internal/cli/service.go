package cli

import (
	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/service"
)

// newServiceCmd is the hidden entrypoint the OS service manager invokes
// to run the embedded relay daemon. Customers never call this directly;
// they install the binary and the service manager spawns it via this
// subcommand. Hidden via cobra.Hidden so it doesn't show up in --help.
func newServiceCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_service",
		Hidden: true,
		Short:  "Service-mode entrypoint. Invoked by the OS service manager. Not for human use.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return service.Run()
		},
	}
}
