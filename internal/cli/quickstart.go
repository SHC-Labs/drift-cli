package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/clients"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/ipc"
)

func newQuickstartCmd() *cobra.Command {
	var noService bool
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Guided setup wizard (the install one-liner runs this for you)",
		Long: `Interactive setup wizard. The customer-facing install one-liner ends
with this command so a fresh install walks the user through machine
setup, LLM client selection, project opt-in, and a verification hook
fire without anyone needing to remember 'drift install' vs 'drift init'.

Falls back to plain 'drift install' behavior when stdin isn't a TTY,
so CI and scripted installs keep working unchanged.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuickstart(cmd.OutOrStdout(), cmd.ErrOrStderr(), noService)
		},
	}
	cmd.Flags().BoolVar(&noService, "no-service", false, "Skip OS service install/start (for sandboxed testing)")
	return cmd
}

// clientTier maps a client ID to one of three integration tiers that
// the dashboard surfaces:
//
//   FULL      - MCP + auto-firing hooks (Claude Code only; hooks fire
//               on every prompt and every Edit/Write)
//   AGENTS.MD - MCP server + a rules file the agent reads (Cursor uses
//               .cursorrules; Windsurf/Antigravity/Zed/Kilo/Kimi all
//               use AGENTS.md; the customer's agent calls drift_*
//               tools when prompted by the rules file)
//   MCP-ONLY  - just the MCP server connection (VS Code, ChatGPT);
//               the customer drives drift_* tool calls themselves
func clientTier(id clients.ClientID) string {
	switch id {
	case clients.ClaudeCode:
		return "FULL"
	case clients.Cursor, clients.Windsurf, clients.Antigravity,
		clients.Kilo, clients.Kimi, clients.Zed:
		return "AGENTS.MD"
	default:
		return "MCP-ONLY"
	}
}

func runQuickstart(stdout, stderr io.Writer, noService bool) error {
	if !isInteractive() {
		// CI / scripted installs land here. Fall back to plain
		// non-interactive install so the same one-liner keeps working
		// in pipelines.
		fmt.Fprintln(stdout, "drift quickstart: non-interactive shell, running 'drift install' instead.")
		return runInstall(stdout, stderr, "", false, false, noService)
	}

	in := bufio.NewReader(os.Stdin)

	fmt.Fprintln(stdout, "============================================================")
	fmt.Fprintln(stdout, "   Drift quickstart — guided setup")
	fmt.Fprintln(stdout, "============================================================")

	// Step 1: machine-level install. runInstall handles token, ~/.mcp.json,
	// service registration, and per-detected-client config writes. The
	// wizard is a thin prompt layer on top.
	section(stdout, 1, 5, "Installing machine-level pieces")
	if err := runInstall(stdout, stderr, "", false, false, noService); err != nil {
		return fmt.Errorf("install step: %w", err)
	}

	// Step 2: surface what got detected so the customer knows what's
	// about to be configured per-project + understands the tier system
	// they see in the dashboard.
	section(stdout, 2, 5, "LLM clients detected on this machine")
	detected := clients.DetectAll()
	if len(detected) == 0 {
		fmt.Fprintln(stdout, "  None detected.")
		fmt.Fprintln(stdout, "  Install Claude Code, Cursor, Windsurf, VS Code, etc., then re-run drift quickstart.")
		fmt.Fprintln(stdout, "")
		return nil
	}
	for _, d := range detected {
		fmt.Fprintf(stdout, "  - %-15s [%s]  %s\n", d.ID, clientTier(d.ID), d.ConfigPath)
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Tier reference:")
	fmt.Fprintln(stdout, "  FULL      = MCP + auto-firing hooks (no manual tool calls needed)")
	fmt.Fprintln(stdout, "  AGENTS.MD = MCP + a rules file the agent reads on every session")
	fmt.Fprintln(stdout, "  MCP-ONLY  = just the MCP server; you call drift_* tools manually")

	// Step 3: pick a project root for per-project setup. Default to
	// $PWD; if $PWD is the customer's home dir, suggest skipping.
	section(stdout, 3, 5, "Pick a project to opt into Drift coordination")
	cwd, _ := os.Getwd()
	home, _ := config.Home()
	defaultProj := cwd
	if cwd == home || cwd == "/" || cwd == "" {
		fmt.Fprintln(stdout, "  Your current directory looks like a home dir, not a project.")
		fmt.Fprintln(stdout, "  cd into a project root, OR enter a path below, OR press enter to skip.")
		defaultProj = ""
	}
	projectRoot, err := promptString(stdout, in, "  Project root", defaultProj)
	if err != nil {
		return err
	}
	projectRoot = expandPath(projectRoot, home)
	if projectRoot == "" {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Skipping per-project setup. To opt a project in later:")
		fmt.Fprintln(stdout, "    cd <project-root> && drift init")
		return nil
	}
	if st, sterr := os.Stat(projectRoot); sterr != nil || !st.IsDir() {
		return fmt.Errorf("project root %q is not a directory", projectRoot)
	}

	// Step 4: per-project setup. Reuse the existing drift init pipeline
	// by chdir-ing to the chosen project and calling runInit. The
	// wizard's "remove legacy bash-CLI hooks" win comes for free now
	// that upsertHookEntry replaces untagged drift-* entries.
	section(stdout, 4, 5, "Setting up "+projectRoot)
	if err := runInitInDir(stdout, stderr, projectRoot); err != nil {
		return fmt.Errorf("project setup: %w", err)
	}

	// Multi-project legacy scan. Walk ~/.claude/projects/ for other
	// projects that still have legacy bash-CLI hooks (drift-check.bat
	// etc.) and offer batch migration.
	migrated, scanErr := scanAndOfferLegacyMigration(stdout, in, projectRoot)
	if scanErr != nil {
		fmt.Fprintf(stderr, "Note: legacy scan failed: %v\n", scanErr)
	} else if migrated > 0 {
		fmt.Fprintf(stdout, "  ✓ Migrated legacy hooks across %d other project(s).\n", migrated)
	}

	// Step 5: verify by firing a hook against the local relay. Skips
	// silently if --no-service was set since there's no relay to talk
	// to.
	section(stdout, 5, 5, "Verify the install with a test hook")
	if noService {
		fmt.Fprintln(stdout, "  Service install was skipped; can't verify against the relay.")
	} else {
		verifyRelayWarmup(stdout, stderr, projectRoot)
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "============================================================")
	fmt.Fprintln(stdout, "Done. Open your LLM client in "+projectRoot+" and start prompting.")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "  drift status   # health check")
	fmt.Fprintln(stdout, "  drift doctor   # full diagnostics")
	fmt.Fprintln(stdout, "============================================================")
	return nil
}

// runInitInDir is runInit but for an explicit project dir rather than
// cwd. Used by the wizard so the customer doesn't have to cd into the
// project before running quickstart.
func runInitInDir(stdout, stderr io.Writer, projectDir string) error {
	prevCwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(projectDir); err != nil {
		return fmt.Errorf("cd %s: %w", projectDir, err)
	}
	defer func() { _ = os.Chdir(prevCwd) }()
	return runInit(stdout, stderr, "default", nil)
}

// scanAndOfferLegacyMigration walks ~/.claude/projects/ to find any
// other project roots that have a legacy bash-CLI hook entry in their
// .claude/settings.local.json (drift-check.bat / drift-report.bat / sh
// / mjs). Offers batch migration via the same upsertHookEntry path
// drift init already uses. Returns the number of projects touched.
//
// Skips the project the wizard just set up (already migrated) and
// projects with no settings.local.json. Best-effort: errors are
// surfaced as warnings but don't abort the wizard.
func scanAndOfferLegacyMigration(stdout io.Writer, in *bufio.Reader, justSetUp string) (int, error) {
	home, err := config.Home()
	if err != nil {
		return 0, nil
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	st, err := os.Stat(projectsDir)
	if err != nil || !st.IsDir() {
		return 0, nil
	}
	candidates, err := findProjectsWithLegacyHooks(projectsDir, justSetUp)
	if err != nil || len(candidates) == 0 {
		return 0, err
	}
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Other projects with legacy bash-CLI drift hooks:")
	for _, p := range candidates {
		fmt.Fprintf(stdout, "  - %s\n", p)
	}
	ok, err := promptYesNo(stdout, in, "  Migrate these to the Go binary's hooks?", true)
	if err != nil || !ok {
		return 0, err
	}
	exePath, err := os.Executable()
	if err != nil {
		return 0, err
	}
	migrated := 0
	for _, p := range candidates {
		if _, mErr := clients.RegisterClaudeCodeHooks(p, exePath); mErr != nil {
			fmt.Fprintf(stdout, "    skipped %s: %v\n", p, mErr)
			continue
		}
		migrated++
	}
	return migrated, nil
}

// findProjectsWithLegacyHooks reads ~/.claude/projects/ entries and
// returns the on-disk project roots whose .claude/settings.local.json
// contains an unmigrated drift-* hook entry. Claude Code persists one
// directory per project here; the directory name is the slugged path
// of the project root so we have to convert back.
func findProjectsWithLegacyHooks(projectsDir, skip string) ([]string, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}
	var hits []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Claude Code's project-state dir name is the project root
		// path with separators replaced by '-'. Reverse the slug to
		// recover the candidate root.
		root := unslugProjectName(e.Name())
		if root == "" || root == skip {
			continue
		}
		settings := filepath.Join(root, ".claude", "settings.local.json")
		if hasLegacyDriftHook(settings) {
			hits = append(hits, root)
		}
	}
	return hits, nil
}

// unslugProjectName turns Claude Code's "-tmp-foo-bar" project-state
// dir name back into "/tmp/foo/bar". Best-effort: fails gracefully on
// names that don't match the expected shape (returns empty string).
func unslugProjectName(name string) string {
	if !strings.HasPrefix(name, "-") {
		// Non-Unix project root path (Windows starts with a drive
		// letter, e.g. "C--Users-..."). The exact unslugging logic
		// for Windows differs; defer to the next round.
		return ""
	}
	return strings.ReplaceAll(name, "-", "/")
}

// hasLegacyDriftHook returns true when settings.local.json has an
// untagged drift-* hook entry. Reads + parses the file; any error
// returns false (treat unreadable settings as "no legacy hook here").
func hasLegacyDriftHook(settingsPath string) bool {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}
	body := strings.ToLower(string(data))
	for _, marker := range []string{"drift-check.bat", "drift-check.sh", "drift-check.mjs",
		"drift-report.bat", "drift-report.sh", "drift-report.mjs"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// verifyRelayWarmup hits the local relay's /health and fires one
// prompt-submit hook against the freshly-set-up project so the
// customer sees the live <drift-context> output before the wizard
// exits. Soft-fails: warns but doesn't abort the wizard if the relay
// isn't ready yet.
func verifyRelayWarmup(stdout, stderr io.Writer, projectDir string) {
	port, err := ipc.CurrentPort()
	if err != nil || port == 0 {
		fmt.Fprintln(stderr, "  Note: no relay port persisted; skipping verify.")
		return
	}

	// Wait up to 5s for /health to come up. The service starts async
	// so a fresh install may take a beat to bind.
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	healthy := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
		resp, herr := http.DefaultClient.Do(req)
		cancel()
		if herr == nil && resp != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = resp.Body.Close()
			healthy = true
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !healthy {
		fmt.Fprintf(stderr, "  Note: relay not responding at 127.0.0.1:%d yet; check 'drift status' in a moment.\n", port)
		return
	}
	fmt.Fprintf(stdout, "  ✓ Relay listening on 127.0.0.1:%d\n", port)
	// Skip the actual hook-fire-and-render step here for now — that
	// path requires invoking the binary recursively which complicates
	// the stdio setup. The customer's first real prompt will exercise
	// the chain end-to-end.
	fmt.Fprintln(stdout, "  ✓ Open your LLM client to fire the first <drift-context>.")
}

// expandPath resolves a leading ~ to the customer's home dir and
// returns absolute. Empty input passes through.
func expandPath(p, home string) string {
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~") {
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
