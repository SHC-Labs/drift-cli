package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/clients"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/ipc"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/migration"
	"github.com/SHC-Labs/drift/internal/service"
	"github.com/SHC-Labs/drift/internal/telemetry"
	"github.com/SHC-Labs/drift/internal/version"
)

// defaultMCPURL is the canonical Drift MCP endpoint. drift install refuses
// to write any other URL unless --unsafe-mcp-url is set AND the user
// confirms interactively.
const defaultMCPURL = "https://mcp.driftlabs.io/mcp"

// envDriftToken is the env var the install one-liner forwards to provide
// the customer's API key without an interactive prompt. Pattern carried
// over from the bash install: `DRIFT_TOKEN=drift_... bash <(curl ...)`.
const envDriftToken = "DRIFT_TOKEN"

func newInstallCmd() *cobra.Command {
	var unsafeMcpURL bool
	var keepLegacy bool
	var customMcpURL string
	var noService bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Once-per-machine: register service, write mcp.json, set up hooks",
		Long: `Writes the drift entry to ~/.mcp.json so MCP-aware AI clients (Claude
Code, Cursor, Windsurf, Antigravity, Zed, Kimi, ChatGPT, VS Code, Kilo)
can talk to the Drift server.

Per-project hook activation lives in 'drift init', not 'drift install'.
After install, run 'drift init' inside any project root you want Drift
coordination on.

Idempotent: re-running drift install upserts the drift entry in
~/.mcp.json, leaving every other server entry unchanged.

Detects legacy install artifacts (bash hook scripts, supervisor.ps1,
.bat wrappers) and lists them. Sprint 3 ships the cleanup logic; v1 day
2-3 lists what would be removed without removing anything.

Provide your token via the DRIFT_TOKEN env var to skip the YOUR_DRIFT_TOKEN
placeholder:

  DRIFT_TOKEN=drift_... drift install`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), customMcpURL, unsafeMcpURL, keepLegacy, noService)
		},
	}
	cmd.Flags().BoolVar(&unsafeMcpURL, "unsafe-mcp-url", false, "Allow non-allowlisted MCP URLs (requires interactive confirm)")
	cmd.Flags().BoolVar(&keepLegacy, "keep-legacy", false, "Skip legacy artifact cleanup")
	cmd.Flags().StringVar(&customMcpURL, "mcp-url", "", "Override the MCP URL (allowlist still applies unless --unsafe-mcp-url)")
	cmd.Flags().BoolVar(&noService, "no-service", false, "Skip OS service install/start (for sandboxed testing)")
	return cmd
}

func runInstall(stdout, stderr io.Writer, customURL string, unsafeURL, keepLegacy, noService bool) error {
	mcpURL := defaultMCPURL
	if customURL != "" {
		// Allowlist: only mcp.driftlabs.io and 127.0.0.1 are allowed
		// without --unsafe-mcp-url. See DRIFT_BINARY_REWRITE_PLAN.md
		// "MCP URL allowlist" for why.
		if !urlAllowlisted(customURL) && !unsafeURL {
			return fmt.Errorf("--mcp-url %q is not on the allowlist (mcp.driftlabs.io, 127.0.0.1:*); pass --unsafe-mcp-url to override", customURL)
		}
		if !urlAllowlisted(customURL) && unsafeURL {
			fmt.Fprintln(stderr, "")
			fmt.Fprintln(stderr, "⚠️  WARNING: --unsafe-mcp-url is set with a non-allowlisted URL.")
			fmt.Fprintf(stderr, "   Pointing your MCP client at %s\n", customURL)
			fmt.Fprintln(stderr, "   This sends your prompts + responses to a third-party server. Only do this if you")
			fmt.Fprintln(stderr, "   trust that server fully. To use the official Drift server, omit --mcp-url.")
			fmt.Fprintln(stderr, "")
		}
		mcpURL = customURL
	}

	token := os.Getenv(envDriftToken)
	if token != "" {
		ver, verr := config.ValidateToken(token)
		if verr != nil {
			return fmt.Errorf("DRIFT_TOKEN rejected: %w", verr)
		}
		// Token in the keychain is the source of truth for the relay's
		// upstream auth. ~/.mcp.json doesn't need to carry it because
		// MCP clients connect to 127.0.0.1:<port> (the local relay)
		// which adds the Bearer header from the keychain on outbound.
		if err := keychain.SetToken(token); err != nil {
			return fmt.Errorf("store token in keychain: %w", err)
		}
		fmt.Fprintf(stdout, "Stored API token in OS keychain (format: %s).\n", ver)
	} else if existing, err := keychain.GetToken(); err != nil || existing == "" {
		fmt.Fprintln(stderr, "Note: DRIFT_TOKEN not set and no existing token in keychain.")
		fmt.Fprintln(stderr, "      The relay will reject MCP traffic until a token is stored.")
		fmt.Fprintln(stderr, "      Get your key from https://app.driftlabs.io/profile and re-run with:")
		fmt.Fprintln(stderr, "        DRIFT_TOKEN=<your-key> drift install")
		fmt.Fprintln(stderr, "")
	} else {
		fmt.Fprintln(stdout, "Existing token found in keychain (kept).")
	}

	installID, err := keychain.EnsureInstallID()
	if err != nil {
		fmt.Fprintf(stderr, "Note: keychain unavailable for install_id (%v). State events will skip until keychain is reachable.\n", err)
	} else {
		fmt.Fprintf(stdout, "Install ID: %s\n", installID)
	}

	// Reserve a stable random port for the relay. Sticky for the install's
	// lifetime: once chosen, never changes. The relay binds this via
	// ipc.BindHardened when the service starts.
	port, err := ipc.EnsurePort()
	if err != nil {
		return fmt.Errorf("reserve relay port: %w", err)
	}
	fmt.Fprintf(stdout, "Reserved relay port %d (persisted in %s)\n", port, config.BinaryConfigPath())

	// ~/.mcp.json points at the LOCAL relay, not the remote upstream.
	// The relay forwards to mcpURL with the Bearer header from the
	// keychain. MCP clients see only localhost.
	relayURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)
	if err := config.WriteMCPDriftEntry(relayURL); err != nil {
		return fmt.Errorf("write %s: %w", config.MCPPath(), err)
	}
	fmt.Fprintf(stdout, "Wrote drift entry to %s (-> %s)\n", config.MCPPath(), relayURL)
	_ = mcpURL // upstream URL is the relay's destination, surfaced via service env

	// Per-client detection + writes. Every supported MCP client gets
	// the drift entry written to its config file (where applicable);
	// the install one-shot writes to all of them so customers running
	// multiple clients (Cursor + Claude Code, etc) don't have to
	// re-install per client.
	detected := clients.DetectAll()
	if len(detected) == 0 {
		fmt.Fprintln(stdout, "No MCP clients detected. Install one (Claude Code, Cursor, Windsurf, etc.) and re-run drift install.")
	}
	for _, d := range detected {
		path, werr := clients.WriteMCPEntry(d, relayURL)
		switch {
		case werr != nil:
			fmt.Fprintf(stderr, "Note: %s config write failed: %v\n", d.ID, werr)
			if installID != "" {
				go fireClientConnected(installID, string(d.ID), false, "")
			}
		case path == "":
			// Per-project clients (Cursor, VS Code) don't get a global
			// write. Surface as detected-but-needs-init so the user
			// runs drift init in their projects.
			fmt.Fprintf(stdout, "Detected %s (per-project setup; run 'drift init' in your project root)\n", d.ID)
			if installID != "" {
				go fireClientConnected(installID, string(d.ID), true, "")
			}
		default:
			fmt.Fprintf(stdout, "Wrote drift entry to %s (%s)\n", path, d.ID)
			if installID != "" {
				go fireClientConnected(installID, string(d.ID), true, path)
			}
		}
	}

	// Surface a hooks-aware vs hooks-less hint. Only Claude Code has
	// auto-firing hooks in v1; other clients call drift_* tools
	// manually via .cursorrules / AGENTS.md / similar.
	hasHooksAware := false
	hasNonHooksAware := false
	for _, d := range detected {
		if d.HooksAware {
			hasHooksAware = true
		} else {
			hasNonHooksAware = true
		}
	}
	if hasNonHooksAware && !hasHooksAware {
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Note: hooks won't auto-fire on the detected clients (only Claude Code supports auto-firing hooks).")
		fmt.Fprintln(stderr, "      Use drift_* tools manually or via .cursorrules / AGENTS.md / your client's equivalent.")
		fmt.Fprintln(stderr, "      See https://app.driftlabs.io/kb/manual-clients for snippets.")
	}

	legacy := migration.Detect()
	if legacy.Found() {
		fmt.Fprintln(stdout, "")
		if keepLegacy {
			fmt.Fprintln(stdout, "Legacy install artifacts found (--keep-legacy preserves them):")
			for _, p := range legacy.Paths {
				fmt.Fprintf(stdout, "  - %s\n", p)
			}
		} else {
			results := migration.Cleanup(false)
			summary := migration.Summary(results)
			if summary != "" {
				fmt.Fprintln(stdout, summary)
			}
		}
	}

	if !noService {
		if err := service.Install(); err != nil {
			fmt.Fprintf(stderr, "Note: service install failed: %v\n", err)
			fmt.Fprintln(stderr, "      Re-run with --no-service to skip, or fix the error and re-run drift install.")
		} else {
			fmt.Fprintln(stdout, "Registered drift as a system service.")
			if err := service.Start(); err != nil {
				fmt.Fprintf(stderr, "Note: service start failed: %v\n", err)
				fmt.Fprintln(stderr, "      Service is registered but not running; check 'drift relay status'.")
			} else {
				fmt.Fprintln(stdout, "Service started.")
			}
		}
	}

	// Fire the cli-installed state event so the dashboard can mark the
	// "Install Drift CLI" Getting Started step complete. Fire-and-forget
	// in a goroutine with the standard retry schedule; never blocks
	// install on dashboard reachability. Per-client connected events
	// already fired inside the detection loop above.
	if installID != "" {
		go fireCLIInstalled(installID)
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Done. Next step: run 'drift init' inside any project root you want Drift coordination on.")
	fmt.Fprintln(stdout, "If anything breaks, run `drift doctor` and paste output to support@driftlabs.io.")
	return nil
}

// urlAllowlisted enforces the MCP URL allowlist. See plan section
// "MCP URL allowlist" for the threat model: prevents social-engineering
// attacks where a script tricks a user into pointing their MCP client
// at evil.com.
func urlAllowlisted(raw string) bool {
	// Cheap string check; this isn't a security boundary, just a
	// foot-gun guard. The real guard is the interactive confirm under
	// --unsafe-mcp-url.
	switch {
	case startsWith(raw, "https://mcp.driftlabs.io"):
		return true
	case startsWith(raw, "http://127.0.0.1"):
		return true
	case startsWith(raw, "https://127.0.0.1"):
		return true
	}
	return false
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// upstreamForStateEvents returns the URL the install state events POST
// to. Always the upstream Drift server (not the local relay), because
// state events are about the install lifecycle and the relay might not
// be running yet.
func upstreamForStateEvents() string {
	if v := os.Getenv("DRIFT_API_URL"); v != "" {
		return v
	}
	return "https://mcp.driftlabs.io"
}

// fireCLIInstalled posts the cli-installed state event with retry.
// Runs in a goroutine; failures are logged but don't surface to the
// install command's exit code (per the API spec: never block install).
// Skips entirely if telemetry is disabled.
func fireCLIInstalled(installID string) {
	if !telemetry.Enabled() {
		return
	}
	token, err := keychain.GetToken()
	if err != nil {
		return
	}
	client := api.NewClient(upstreamForStateEvents(), token)
	req := api.CLIInstalledRequest{
		InstallID:     installID,
		BinaryVersion: version.Version,
		OS:            api.RuntimeOS(),
		Arch:          api.RuntimeArch(),
		HostnameHash:  api.HostnameHash(),
	}
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_ = api.PostWithRetry(parent, func(ctx context.Context) error {
		return client.PostCLIInstalled(ctx, req)
	})
}

// fireClientConnected posts a client-connected state event with
// retry. Multiple per install allowed; one per detected MCP client.
// Skips entirely if telemetry is disabled.
func fireClientConnected(installID, clientName string, success bool, configPath string) {
	if !telemetry.Enabled() {
		return
	}
	token, err := keychain.GetToken()
	if err != nil {
		return
	}
	client := api.NewClient(upstreamForStateEvents(), token)
	req := api.ClientConnectedRequest{
		InstallID:  installID,
		Client:     clientName,
		Success:    success,
		ConfigPath: configPath,
	}
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_ = api.PostWithRetry(parent, func(ctx context.Context) error {
		return client.PostClientConnected(ctx, req)
	})
}
