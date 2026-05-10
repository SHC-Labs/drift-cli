package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/keychain"
)

// newTokenCmd is the parent for token-management subcommands. Today
// only `drift token set` ships; future subcommands (status, clear,
// rotate) can land here. We deliberately do not expose a `show`
// subcommand: pulling the full token to stdout is the kind of thing
// that ends up in shell history or a screen recording.
func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage the API token in the OS keychain",
		Long: `Token-management subcommands. Lets you swap the keychain entry
without re-running the full install one-liner. Useful after a
rotation in the dashboard.

For where the token lives per OS, how to view it, and the full
rotation flow, see https://app.driftlabs.io/kb/api-key-management.`,
	}
	cmd.AddCommand(newTokenSetCmd())
	return cmd
}

// newTokenSetCmd is the W9.1 add. Drops the customer into a masked TUI
// prompt, validates the pasted token against config.ValidateToken, and
// writes it into the OS keychain via keychain.SetToken. Non-TTY callers
// get an error telling them to use DRIFT_TOKEN env var instead so we
// never silently no-op in CI.
func newTokenSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set",
		Short: "Paste a fresh API token into the OS keychain",
		Long: `Replaces the current token in your OS keychain (service "drift",
account "token") with one you paste interactively. Run after rotating
your token at https://app.driftlabs.io/profile.

Stdin must be a TTY: the prompt is masked so the token doesn't end up
in your shell history or a screen recording. For headless or scripted
flows, set DRIFT_TOKEN in the environment and re-run the install
one-liner instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTokenSet(cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func runTokenSet(stdout, stderr io.Writer) error {
	if !isInteractive() {
		fmt.Fprintln(stderr, "drift token set needs a TTY (stdin is piped or redirected).")
		fmt.Fprintln(stderr, "For scripted flows, set DRIFT_TOKEN in the environment and re-run:")
		fmt.Fprintln(stderr, "  DRIFT_TOKEN=<your-token> drift install")
		return errors.New("non-interactive shell: cannot prompt for token")
	}

	var token string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Set Drift API token").
				Description("Paste a fresh token from https://app.driftlabs.io/profile.\nInput is masked. Press ESC to cancel."),
			huh.NewInput().
				Title("DRIFT_TOKEN").
				EchoMode(huh.EchoModePassword).
				Value(&token).
				Validate(func(s string) error {
					s = strings.TrimSpace(s)
					if s == "" {
						return errors.New("token cannot be empty")
					}
					if _, err := config.ValidateToken(s); err != nil {
						return err
					}
					return nil
				}),
		),
	)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Fprintln(stdout, "Cancelled. Keychain unchanged.")
			return nil
		}
		return fmt.Errorf("prompt: %w", err)
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("no token provided")
	}

	if err := keychain.SetToken(token); err != nil {
		return fmt.Errorf("store token in keychain: %w", err)
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Token stored in OS keychain.")
	fmt.Fprintln(stdout, "  Verify with: drift status")
	fmt.Fprintln(stdout, "")
	if !keychainBackendLikelyUsable() {
		fmt.Fprintln(stderr, "Note: this OS may not have a usable keystore backend (headless Linux without gnome-keyring).")
		fmt.Fprintln(stderr, "      drift status will tell you if the token actually landed.")
	}
	return nil
}

// keychainBackendLikelyUsable checks whether the keychain Set we just
// did is likely to persist across restarts. On macOS / Windows the
// answer is always yes. On Linux without a Secret Service, go-keyring
// returns success but the value is in-process only and lost on relay
// restart, which v0.1.18 cannot detect cheaply. The v0.1.19 file
// fallback (W9.2) replaces this heuristic with a real backend check.
func keychainBackendLikelyUsable() bool {
	if os.Getenv("DRIFT_FORCE_FILE_KEYSTORE") != "" {
		return false
	}
	return true
}
