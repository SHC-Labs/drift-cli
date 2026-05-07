package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/clients"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/ipc"
)

func newQuickstartCmd() *cobra.Command {
	var noService bool
	var forceInline bool
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Guided setup wizard (the install one-liner runs this for you)",
		Long: `Interactive setup wizard. The customer-facing install one-liner ends
with this command so a fresh install walks the user through machine
setup, LLM client selection, project opt-in, and a verification hook
fire without anyone needing to remember 'drift install' vs 'drift init'.

When stdin is a real TTY, runs a full-screen TUI form (multi-select,
text input, progress) via charmbracelet/huh. When stdin is piped or
redirected (CI), falls back to plain 'drift install' so scripted
installs keep working unchanged.

Use --inline to force the line-prompt style even on a TTY (useful for
debugging or when the TUI doesn't render well over a remote shell).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuickstart(cmd.OutOrStdout(), cmd.ErrOrStderr(), noService, forceInline)
		},
	}
	cmd.Flags().BoolVar(&noService, "no-service", false, "Skip OS service install/start (for sandboxed testing)")
	cmd.Flags().BoolVar(&forceInline, "inline", false, "Use line-prompt style instead of the TUI form")
	return cmd
}

// clientTier maps a client ID to one of three integration tiers that
// the dashboard surfaces:
//
//	FULL      - MCP + auto-firing hooks (Claude Code only; hooks fire
//	            on every prompt and every Edit/Write)
//	AGENTS.MD - MCP server + a rules file the agent reads (Cursor uses
//	            .cursorrules; Windsurf/Antigravity/Zed/Kilo/Kimi all
//	            use AGENTS.md; the customer's agent calls drift_*
//	            tools when prompted by the rules file)
//	MCP-ONLY  - just the MCP server connection (VS Code, ChatGPT);
//	            the customer drives drift_* tool calls themselves
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

func runQuickstart(stdout, stderr io.Writer, noService, forceInline bool) error {
	// Non-TTY path: CI / scripted installs land here. Run plain install
	// so the same one-liner keeps working in pipelines.
	if !isInteractive() {
		fmt.Fprintln(stdout, "drift quickstart: non-interactive shell, running 'drift install' instead.")
		return runInstall(stdout, stderr, "", false, false, noService)
	}
	if forceInline {
		return runQuickstartInline(stdout, stderr, noService)
	}
	return runQuickstartTUI(stdout, stderr, noService)
}

// runQuickstartTUI is the polished form-based wizard. Uses huh for the
// multi-select + text input + confirm. Falls back to runQuickstartInline
// if the TUI fails to render (e.g. terminal doesn't support the ANSI
// sequences huh expects).
func runQuickstartTUI(stdout, stderr io.Writer, noService bool) error {
	detected := clients.DetectAll()
	if len(detected) == 0 {
		// Nothing to multi-select; just run the install and tell the
		// user to install a client + re-run quickstart.
		fmt.Fprintln(stdout, "No LLM clients detected. Installing machine-level pieces only.")
		fmt.Fprintln(stdout, "Install Claude Code, Cursor, Windsurf, etc., then re-run drift quickstart.")
		return runInstall(stdout, stderr, "", false, false, noService)
	}

	// Build the multi-select options. Default-select all detected
	// clients so the customer's first instinct (hit enter) does the
	// right thing for the common case where they want everything wired.
	options := make([]huh.Option[string], 0, len(detected))
	for _, d := range detected {
		label := fmt.Sprintf("%s  [%s]", d.ID, clientTier(d.ID))
		options = append(options, huh.NewOption(label, string(d.ID)).Selected(true))
	}

	cwd, _ := os.Getwd()
	home, _ := config.Home()
	defaultProject := cwd
	skipProject := false
	if cwd == home || cwd == "/" || cwd == "" {
		// Looks like the customer ran the wizard from their home dir
		// or some non-project location. Keep the default empty so the
		// project-root prompt forces a deliberate choice.
		defaultProject = ""
	}

	selected := make([]string, 0, len(options))
	projectRoot := defaultProject
	confirmGo := true

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Drift Setup").
				Description("This wizard will:\n  1. Install machine-level pieces (token, MCP, service)\n  2. Configure your LLM client(s)\n  3. Opt a project into Drift\n  4. Verify with a test hook\n\nPress enter to continue."),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Pick LLM clients to configure").
				Description("FULL = MCP + auto-firing hooks\nAGENTS.MD = MCP + a rules file the agent reads\nMCP-ONLY = just the MCP server connection").
				Options(options...).
				Value(&selected).
				Filterable(false),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Project root to opt into Drift").
				Description("Press enter to skip the per-project step.").
				Value(&projectRoot).
				Validate(validateProjectRoot),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Ready to install?").
				Description("Drift will configure the selected client(s) and opt the project in.").
				Affirmative("Install").
				Negative("Cancel").
				Value(&confirmGo),
		),
	)

	if err := form.Run(); err != nil {
		// huh returns ErrUserAborted on ESC. Treat as a clean cancel.
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Fprintln(stdout, "Cancelled.")
			return nil
		}
		// Other errors: TUI didn't work. Drop to the inline wizard so
		// the customer still has a path forward.
		fmt.Fprintf(stderr, "Note: TUI form failed (%v); falling back to inline prompts.\n", err)
		return runQuickstartInline(stdout, stderr, noService)
	}

	if !confirmGo {
		fmt.Fprintln(stdout, "Cancelled.")
		return nil
	}
	if projectRoot == "" {
		skipProject = true
	}

	// Convert the selected []string of client IDs back to []clients.ClientID
	// so SetupProjectFiltered can use it.
	only := make([]clients.ClientID, 0, len(selected))
	for _, s := range selected {
		only = append(only, clients.ClientID(s))
	}

	return runWizardSteps(stdout, stderr, noService, projectRoot, skipProject, only)
}

// runQuickstartInline is the line-prompt fallback wizard. Same flow as
// the TUI; just no boxes or arrow-key navigation. Used by --inline and
// when the TUI fails to render.
func runQuickstartInline(stdout, stderr io.Writer, noService bool) error {
	in := bufio.NewReader(os.Stdin)

	fmt.Fprintln(stdout, "============================================================")
	fmt.Fprintln(stdout, "   Drift quickstart — guided setup")
	fmt.Fprintln(stdout, "============================================================")

	detected := clients.DetectAll()
	section(stdout, 1, 4, "LLM clients detected on this machine")
	if len(detected) == 0 {
		fmt.Fprintln(stdout, "  None detected.")
	} else {
		for _, d := range detected {
			fmt.Fprintf(stdout, "  - %-15s [%s]  %s\n", d.ID, clientTier(d.ID), d.ConfigPath)
		}
	}
	only := make([]clients.ClientID, 0, len(detected))
	for _, d := range detected {
		ok, err := promptYesNo(stdout, in, fmt.Sprintf("  Configure %s?", d.ID), true)
		if err != nil {
			return err
		}
		if ok {
			only = append(only, d.ID)
		}
	}

	section(stdout, 2, 4, "Pick a project to opt into Drift")
	cwd, _ := os.Getwd()
	home, _ := config.Home()
	defaultProj := cwd
	if cwd == home || cwd == "/" || cwd == "" {
		fmt.Fprintln(stdout, "  Your current directory looks like a home dir.")
		fmt.Fprintln(stdout, "  Enter a project path or hit enter to skip.")
		defaultProj = ""
	}
	projectRoot, err := promptString(stdout, in, "  Project root", defaultProj)
	if err != nil {
		return err
	}
	projectRoot = expandPath(projectRoot, home)
	skipProject := projectRoot == ""

	return runWizardSteps(stdout, stderr, noService, projectRoot, skipProject, only)
}

// runWizardSteps is the shared post-prompt phase for both the TUI and
// inline wizards: install + project setup + multi-project legacy scan
// + relay verify. Both paths feed it the same parameters.
func runWizardSteps(stdout, stderr io.Writer, noService bool, projectRoot string, skipProject bool, only []clients.ClientID) error {
	section(stdout, 1, 4, "Installing machine-level pieces")
	if err := runInstall(stdout, stderr, "", false, false, noService); err != nil {
		return fmt.Errorf("install step: %w", err)
	}

	if skipProject {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "No project chosen. Run 'drift init' inside any project root to opt it in later.")
		return nil
	}

	if st, sterr := os.Stat(projectRoot); sterr != nil || !st.IsDir() {
		return fmt.Errorf("project root %q is not a directory", projectRoot)
	}

	section(stdout, 2, 4, "Setting up "+projectRoot)
	if err := runInitInDirFiltered(stdout, stderr, projectRoot, only); err != nil {
		return fmt.Errorf("project setup: %w", err)
	}

	// Multi-project legacy scan. Walk ~/.claude/projects/ for other
	// project roots that still have legacy bash-CLI hooks and offer
	// batch migration. Best-effort; failures don't abort the wizard.
	section(stdout, 3, 4, "Scan for other projects with legacy hooks")
	migrated, scanErr := scanAndOfferLegacyMigrationAuto(stdout, projectRoot)
	if scanErr != nil {
		fmt.Fprintf(stderr, "Note: legacy scan failed: %v\n", scanErr)
	} else if migrated > 0 {
		fmt.Fprintf(stdout, "  ✓ Migrated legacy hooks across %d other project(s).\n", migrated)
	} else {
		fmt.Fprintln(stdout, "  No other projects need migration.")
	}

	section(stdout, 4, 4, "Verify the install with a test hook")
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

// runInitInDirFiltered runs the equivalent of `drift init` in projectDir
// with the given client allowlist. Saves the wizard from forcing the
// customer to cd into the project before launching.
func runInitInDirFiltered(stdout, stderr io.Writer, projectDir string, only []clients.ClientID) error {
	prevCwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(projectDir); err != nil {
		return fmt.Errorf("cd %s: %w", projectDir, err)
	}
	defer func() { _ = os.Chdir(prevCwd) }()
	return runInitFiltered(stdout, stderr, "default", nil, only)
}

// scanAndOfferLegacyMigrationAuto walks ~/.claude/projects/ to find any
// other project roots that have a legacy bash-CLI hook entry. Migrates
// them automatically (no second prompt — the customer already opted into
// the wizard, asking again per-project is annoying). Returns count of
// projects touched.
func scanAndOfferLegacyMigrationAuto(stdout io.Writer, justSetUp string) (int, error) {
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

// verifyRelayWarmup hits the local relay's /health and prints a
// confirmation. Soft-fails: warns but doesn't abort the wizard if the
// relay isn't ready yet.
func verifyRelayWarmup(stdout, stderr io.Writer, projectDir string) {
	port, err := ipc.CurrentPort()
	if err != nil || port == 0 {
		fmt.Fprintln(stderr, "  Note: no relay port persisted; skipping verify.")
		return
	}

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
	fmt.Fprintln(stdout, "  ✓ Open your LLM client to fire the first <drift-context>.")
}

// validateProjectRoot is the huh.Input.Validate callback for the
// project-root field. Empty string is acceptable (means "skip
// per-project step"); non-empty must point at an existing directory.
func validateProjectRoot(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	home, _ := config.Home()
	if strings.HasPrefix(s, "~") {
		s = filepath.Join(home, strings.TrimPrefix(s, "~"))
	}
	if _, err := os.Stat(s); err != nil {
		return fmt.Errorf("path not found: %s", s)
	}
	return nil
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
