package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/keychain"
)

const dashboardURL = "https://app.driftlabs.io"

func newLoginCmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "OAuth PKCE flow, store token in OS keychain",
		Long: `Opens your browser to the Drift dashboard, lands a one-time auth code
back at a localhost callback, and exchanges it for an API token. Token
goes into your OS keychain (Keychain on Mac, Credential Manager on
Windows, Secret Service on Linux).

Run this once per machine. After login, drift install + drift init
work without DRIFT_TOKEN env var.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), noBrowser)
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print URL instead of opening a browser (for headless servers)")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear keychain entry, retain config",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := keychain.DeleteToken(); err != nil {
				return fmt.Errorf("clear keychain token: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Cleared API token from keychain.")
			return nil
		},
	}
}

func runLogin(ctx context.Context, stdout, stderr io.Writer, noBrowser bool) error {
	apiBase := dashboardURL
	if v := os.Getenv("DRIFT_API_URL"); v != "" {
		apiBase = v
	}

	loginCtx, cancel := context.WithTimeout(ctx, api.LoginTimeout)
	defer cancel()

	var opener func(string) error
	if noBrowser {
		opener = func(url string) error {
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, "Open this URL in your browser:")
			fmt.Fprintln(stdout, "  ", url)
			fmt.Fprintln(stdout, "")
			fmt.Fprintln(stdout, "Waiting for callback...")
			return nil
		}
	} else {
		opener = func(url string) error {
			fmt.Fprintln(stdout, "Opening browser to authenticate...")
			return openBrowser(url)
		}
	}

	result, err := api.Login(loginCtx, apiBase, opener)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("login timed out after %v. Try again or use --no-browser on headless servers", api.LoginTimeout)
		}
		return fmt.Errorf("login: %w", err)
	}

	// Validate token format before storing.
	if _, verr := config.ValidateToken(result.Token); verr != nil {
		return fmt.Errorf("server returned malformed token: %w", verr)
	}

	if err := keychain.SetToken(result.Token); err != nil {
		return fmt.Errorf("store token in keychain: %w", err)
	}

	if _, err := keychain.EnsureInstallID(); err != nil {
		fmt.Fprintf(stderr, "Note: install_id keychain set failed: %v (state events will skip)\n", err)
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Logged in. Token stored in OS keychain.")
	if result.UserEmail != "" {
		fmt.Fprintf(stdout, "  user: %s\n", result.UserEmail)
	}
	if result.OrgName != "" {
		fmt.Fprintf(stdout, "  org:  %s\n", result.OrgName)
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Run 'drift install' to set up the relay + per-client configs.")

	// Suppress the unused-import warning when LoginTimeout is removed.
	_ = time.Second
	return nil
}

// openBrowser opens the OS default browser to url. Per-platform
// commands; we don't want to take a dep on go-open or similar for one
// function.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
