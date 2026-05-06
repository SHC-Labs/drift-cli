package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/service"
)

func newUninstallCmd() *cobra.Command {
	var keepConfigs bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove service, configs, and keychain entry",
		Long: `Stops the relay service, removes the service registration, deletes the
keychain entries (token + install_id + ECDH privkey), and removes the
binary's config files (~/.drift/config.json, ~/.mcp.json drift entry).

--keep-configs preserves config files for re-installation.

Idempotent: re-running on a partially-uninstalled state finishes the job.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), keepConfigs)
		},
	}
	cmd.Flags().BoolVar(&keepConfigs, "keep-configs", false, "Preserve config files for re-install")
	return cmd
}

func runUninstall(stdout, stderr io.Writer, keepConfigs bool) error {
	// Stop service first so the relay isn't running while we yank
	// keychain entries out from under it.
	if err := service.Stop(); err != nil {
		fmt.Fprintf(stderr, "Note: stop service: %v (continuing)\n", err)
	} else {
		fmt.Fprintln(stdout, "Stopped service.")
	}
	if err := service.Uninstall(); err != nil {
		fmt.Fprintf(stderr, "Note: uninstall service: %v (continuing)\n", err)
	} else {
		fmt.Fprintln(stdout, "Removed service registration.")
	}

	// Drop keychain entries. Best-effort: if the keychain is locked or
	// otherwise unavailable, we still want the rest of uninstall to run.
	for _, item := range []struct {
		key string
		fn  func() error
	}{
		{"token", keychain.DeleteToken},
		{"install_id", keychain.DeleteInstallID},
		{"privkey", keychain.DeletePrivKey},
	} {
		if err := item.fn(); err != nil {
			fmt.Fprintf(stderr, "Note: delete keychain %s: %v (continuing)\n", item.key, err)
		}
	}
	fmt.Fprintln(stdout, "Cleared keychain entries (token, install_id, privkey).")

	if !keepConfigs {
		if err := os.Remove(config.BinaryConfigPath()); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "Note: remove %s: %v\n", config.BinaryConfigPath(), err)
		} else {
			fmt.Fprintf(stdout, "Removed %s\n", config.BinaryConfigPath())
		}
		if err := removeMCPDriftEntry(); err != nil {
			fmt.Fprintf(stderr, "Note: clean ~/.mcp.json: %v\n", err)
		} else {
			fmt.Fprintf(stdout, "Removed drift entry from %s\n", config.MCPPath())
		}
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "drift uninstalled.")
	return nil
}

// removeMCPDriftEntry deletes only the "drift" entry from
// mcpServers in ~/.mcp.json, leaving other servers + top-level keys
// alone. If removing drift leaves mcpServers empty, the empty map
// stays (avoids the user wondering why the file disappeared).
func removeMCPDriftEntry() error {
	path := config.MCPPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	root := map[string]any{}
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	rawServers, ok := root["mcpServers"]
	if !ok {
		return nil
	}
	servers, ok := rawServers.(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := servers["drift"]; !ok {
		return nil
	}
	delete(servers, "drift")
	root["mcpServers"] = servers

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return config.AtomicWriteFile(path, out, 0o600)
}
