package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/service"
	"github.com/SHC-Labs/drift/internal/update"
	"github.com/SHC-Labs/drift/internal/version"
)

const updateBaseURL = "https://mcp.driftlabs.io"

func newUpdateCmd() *cobra.Command {
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Atomic self-update, verifies cosign signature",
		Long: `Checks the Drift release server for a newer binary, downloads it,
verifies the SHA-256 checksum + cosign signature, and atomically
replaces the running binary. Restarts the service afterward.

--check only checks for updates without applying.

Set DRIFT_REQUIRE_COSIGN=1 to refuse updates without a valid cosign
signature (defense-in-depth against compromised release pipelines).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), checkOnly)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Check for updates without applying")
	return cmd
}

func runUpdate(ctx context.Context, stdout, stderr io.Writer, checkOnly bool) error {
	base := updateBaseURL
	if v := os.Getenv("DRIFT_API_URL"); v != "" {
		base = v
	}
	check, err := update.CheckForUpdate(ctx, base, version.Version)
	if err != nil {
		return fmt.Errorf("check for update: %w", err)
	}

	fmt.Fprintf(stdout, "Current: %s\n", check.CurrentVersion)
	fmt.Fprintf(stdout, "Latest:  %s\n", check.LatestVersion)

	if !check.HasUpdate {
		fmt.Fprintln(stdout, "Already up to date.")
		return nil
	}

	if checkOnly {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "An update is available. Run 'drift update' (without --check) to apply.")
		return nil
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Downloading + verifying...")
	if err := update.Apply(ctx, check); err != nil {
		return fmt.Errorf("apply update: %w", err)
	}
	fmt.Fprintln(stdout, "Update applied. Restarting service...")

	// Stop + start cycle so the new binary takes over. Best-effort:
	// service may not be installed (drift install --no-service) or
	// may be running under a service manager that doesn't accept
	// stop signals (manual launch). Either way the customer can
	// manually restart from the OS service manager.
	if err := service.Stop(); err != nil {
		fmt.Fprintf(stderr, "Note: service stop failed: %v (manual restart needed)\n", err)
	}
	if err := service.Start(); err != nil {
		fmt.Fprintf(stderr, "Note: service start failed: %v (run 'drift install' to re-register)\n", err)
	}
	fmt.Fprintln(stdout, "Done.")
	return nil
}
