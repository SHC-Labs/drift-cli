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
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	// Walk up first to find an existing .drift.json (mirrors the bash
	// `drift project enable`'s upsert behavior). If none, create in cwd.
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
	results, err := clients.SetupProject(projectDir, relayURL, exePath)
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

// writeDriftConfig serializes cfg as pretty-printed JSON and writes
// atomically. Manual marshal so the field order matches the bash
// helper's output (cosmetic; the read path doesn't care).
func writeDriftConfig(path string, cfg *config.DriftConfig) error {
	out := struct {
		Version     int      `json:"version"`
		Enabled     bool     `json:"enabled"`
		Mode        string   `json:"mode,omitempty"`
		DeniedTools []string `json:"denied_tools,omitempty"`
		ReportEdits bool     `json:"report_edits"`
	}{
		Version:     cfg.Version,
		Enabled:     cfg.Enabled,
		Mode:        cfg.Mode,
		DeniedTools: cfg.DeniedTools,
		ReportEdits: cfg.ReportEdits,
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return config.AtomicWriteFile(path, data, 0o644)
}
