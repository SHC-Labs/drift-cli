package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/clients"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/ipc"
)

// driftConfigVersion is the schema version drift init writes into
// .drift.json. Read-side migrations live in internal/config/drift.go.
const driftConfigVersion = 1

func newInitCmd() *cobra.Command {
	var mode string
	var deny []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Once-per-project: write .drift.json, register hook entries with Claude Code",
		Long: `Opts the current directory in to Drift coordination by writing
.drift.json and (when Claude Code is detected) registering hook entries
in <project>/.claude/settings.local.json that fire 'drift internal hook
prompt-submit' on every prompt and 'drift internal hook post-tool-use'
on every Edit / Write.

Run from the project root (or any subdirectory; we walk up to find an
existing .drift.json before creating a new one in the current dir).

Idempotent: re-running upserts the .drift.json fields and the hook
entries without duplicating.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd.OutOrStdout(), cmd.ErrOrStderr(), mode, deny)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "default", "Project mode: default | isolated")
	cmd.Flags().StringSliceVar(&deny, "deny", nil, "Tools to deny (comma-separated, e.g. drift_broadcast_change)")
	return cmd
}

func newUninitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninit",
		Short: "Reverse drift init for the current project",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninit(cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func runInit(stdout, stderr io.Writer, mode string, deny []string) error {
	return runInitFiltered(stdout, stderr, mode, deny, nil)
}

// runInitFiltered is runInit with an optional client allowlist. Used by
// the quickstart wizard when the customer narrowed the multi-select to
// a subset of detected clients. A nil/empty filter preserves the
// current "set up every detected client" behavior.
func runInitFiltered(stdout, stderr io.Writer, mode string, deny []string, only []clients.ClientID) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	// Walk up first to find an existing .drift.json (upsert behavior:
	// re-running drift init from anywhere within an already-onboarded
	// project updates the existing file rather than creating a new one).
	// If none found, create in cwd.
	driftPath, err := config.WalkUpForDrift(cwd)
	if errors.Is(err, config.ErrDriftConfigNotFound) {
		driftPath = filepath.Join(cwd, ".drift.json")
	} else if err != nil {
		return fmt.Errorf("walk up for .drift.json: %w", err)
	}
	projectDir := filepath.Dir(driftPath)

	cfg := &config.DriftConfig{
		Version:     driftConfigVersion,
		Enabled:     true,
		Mode:        mode,
		DeniedTools: deny,
		ReportEdits: true,
	}
	if err := writeDriftConfig(driftPath, cfg); err != nil {
		return fmt.Errorf("write %s: %w", driftPath, err)
	}
	fmt.Fprintf(stdout, "Wrote %s (mode=%s, enabled=true)\n", driftPath, mode)

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate drift binary: %w", err)
	}

	// Need the local relay URL for per-project mcp.json files (Cursor,
	// VS Code etc). Read from the binary config; if no port is set,
	// the customer hasn't run drift install yet.
	relayPort, err := ipc.CurrentPort()
	if err != nil || relayPort == 0 {
		return fmt.Errorf("no relay port persisted; run 'drift install' first")
	}
	relayURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", relayPort)

	// Multi-client setup: every detected MCP client gets the right
	// per-project config + hint file written. Claude Code gets hooks;
	// the others get .cursorrules / AGENTS.md sections describing the
	// drift_* tools the agent should call manually.
	results, err := clients.SetupProjectFiltered(projectDir, relayURL, exePath, only)
	if err != nil {
		return fmt.Errorf("setup project: %w", err)
	}
	if len(results) == 0 {
		fmt.Fprintln(stderr, "Note: no MCP clients detected. Drift project file written but no client configs touched.")
		fmt.Fprintln(stderr, "      Install Claude Code, Cursor, Windsurf, etc. and re-run drift init.")
	}
	hooksAware := false
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(stderr, "Note: %s setup error: %v\n", r.ID, r.Err)
			continue
		}
		if r.HookPath != "" {
			fmt.Fprintf(stdout, "  %s: registered hooks in %s\n", r.ID, r.HookPath)
			hooksAware = true
		}
		if r.ConfigPath != "" {
			fmt.Fprintf(stdout, "  %s: wrote %s\n", r.ID, r.ConfigPath)
		}
		if r.HintPath != "" {
			fmt.Fprintf(stdout, "  %s: updated %s with drift tool instructions\n", r.ID, r.HintPath)
		}
	}
	if !hooksAware && len(results) > 0 {
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Note: hooks won't auto-fire on this project's clients (only Claude Code supports auto-firing hooks).")
		fmt.Fprintln(stderr, "      Your agent will call drift_* tools when prompted by .cursorrules / AGENTS.md instructions.")
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Drift enabled in this project.")
	return nil
}

func runUninit(stdout, stderr io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	driftPath, err := config.WalkUpForDrift(cwd)
	if errors.Is(err, config.ErrDriftConfigNotFound) {
		fmt.Fprintln(stdout, "No .drift.json found. Already uninited.")
		return nil
	}
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(driftPath)

	if err := os.Remove(driftPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", driftPath, err)
	}
	fmt.Fprintf(stdout, "Removed %s\n", driftPath)

	results := clients.RemoveProjectSetup(projectDir)
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(stderr, "Note: %s teardown error: %v\n", r.ID, r.Err)
			continue
		}
		if r.HookPath != "" {
			fmt.Fprintf(stdout, "  %s: cleaned %s\n", r.ID, r.HookPath)
		}
		if r.ConfigPath != "" {
			fmt.Fprintf(stdout, "  %s: removed drift entry from %s\n", r.ID, r.ConfigPath)
		}
		if r.HintPath != "" {
			fmt.Fprintf(stdout, "  %s: removed drift section from %s\n", r.ID, r.HintPath)
		}
	}

	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "Drift disabled in this project.")
	return nil
}

// writeDriftConfig writes cfg to .drift.json atomically. Read-modify-
// write through a raw map so unknown top-level fields are preserved:
// a customer's `.drift.json` may contain forward-compat fields meant
// for a future drift version, or fields used by other tools that
// piggy-back on the file. Earlier revisions of this function rebuilt
// from a fixed struct and silently dropped those fields.
func writeDriftConfig(path string, cfg *config.DriftConfig) error {
	root := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(existing, &root)
	}
	setField := func(key string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		root[key] = raw
		return nil
	}
	if err := setField("version", cfg.Version); err != nil {
		return err
	}
	if err := setField("enabled", cfg.Enabled); err != nil {
		return err
	}
	if cfg.Mode != "" {
		if err := setField("mode", cfg.Mode); err != nil {
			return err
		}
	} else {
		delete(root, "mode")
	}
	if len(cfg.DeniedTools) > 0 {
		if err := setField("denied_tools", cfg.DeniedTools); err != nil {
			return err
		}
	} else {
		delete(root, "denied_tools")
	}
	if err := setField("report_edits", cfg.ReportEdits); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return config.AtomicWriteFile(path, data, 0o644)
}
