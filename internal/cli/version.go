package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/version"
)

func newVersionCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version, OS-arch, protocol version, build date",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := map[string]any{
				"version":           version.Version,
				"commit":            version.Commit,
				"build_date":        version.BuildDate,
				"os_arch":           version.OSArch,
				"go_version":        version.GoVersion,
				"protocol_versions": version.ProtocolVersions,
				"aead_algorithms":   version.AEADAlgorithms,
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "drift %s (%s) %s\n", version.Version, version.Commit, version.BuildDate)
			fmt.Fprintf(cmd.OutOrStdout(), "  os/arch:           %s\n", version.OSArch)
			fmt.Fprintf(cmd.OutOrStdout(), "  go:                %s\n", version.GoVersion)
			fmt.Fprintf(cmd.OutOrStdout(), "  protocol versions: %v\n", version.ProtocolVersions)
			fmt.Fprintf(cmd.OutOrStdout(), "  aead algorithms:   %v\n", version.AEADAlgorithms)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return cmd
}
