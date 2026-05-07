// Package doctor produces the diagnostics dump for support tickets.
// Output includes binary version, server reachability, token validity,
// project status (cwd .drift.json walkup), per-client hook health,
// service status, last 50 log lines.
//
// Customers fix 80% of their own problems with this dump. The other
// 20% paste it to hello@driftlabs.io.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SHC-Labs/drift/internal/clients"
	"github.com/SHC-Labs/drift/internal/config"
	"github.com/SHC-Labs/drift/internal/ipc"
	"github.com/SHC-Labs/drift/internal/keychain"
	driftlog "github.com/SHC-Labs/drift/internal/log"
	"github.com/SHC-Labs/drift/internal/migration"
	"github.com/SHC-Labs/drift/internal/service"
	"github.com/SHC-Labs/drift/internal/telemetry"
	"github.com/SHC-Labs/drift/internal/version"
)

// Report is the structured doctor output. Marshaled as pretty JSON
// for the --json flag; the human-readable text view is rendered via
// FormatText.
type Report struct {
	Binary      BinaryInfo      `json:"binary"`
	Service     ServiceInfo     `json:"service"`
	Relay       RelayInfo       `json:"relay"`
	Token       TokenInfo       `json:"token"`
	Project     ProjectInfo     `json:"project"`
	MCPConfig   MCPConfigInfo   `json:"mcp_config"`
	Clients     []ClientInfo    `json:"clients"`
	Telemetry   TelemetryInfo   `json:"telemetry"`
	Legacy      LegacyInfo      `json:"legacy_artifacts"`
	RecentLogs  []string        `json:"recent_logs,omitempty"`
}

type BinaryInfo struct {
	Version          string   `json:"version"`
	Commit           string   `json:"commit"`
	BuildDate        string   `json:"build_date"`
	OSArch           string   `json:"os_arch"`
	GoVersion        string   `json:"go_version"`
	ProtocolVersions []string `json:"protocol_versions"`
	AEADAlgorithms   []string `json:"aead_algorithms"`
	Path             string   `json:"path"`
}

type ServiceInfo struct {
	State string `json:"state"` // running | stopped | unknown
	Err   string `json:"error,omitempty"`
}

type RelayInfo struct {
	Port         int    `json:"port"`
	HealthOK     bool   `json:"health_ok"`
	HealthDetail string `json:"health_detail,omitempty"`
	ConfigError  string `json:"config_error,omitempty"` // populated when ~/.drift/config.json is corrupt
}

type TokenInfo struct {
	InKeychain bool   `json:"in_keychain"`
	Format     string `json:"format,omitempty"` // v1 | legacy | "" if missing
	InstallID  string `json:"install_id,omitempty"`
}

type ProjectInfo struct {
	CWD          string `json:"cwd"`
	DriftJSONPath string `json:"drift_json_path,omitempty"`
	Enabled      bool   `json:"enabled"`
	Mode         string `json:"mode,omitempty"`
}

type MCPConfigInfo struct {
	Path     string `json:"path"`
	Present  bool   `json:"present"`
	URL      string `json:"url,omitempty"`
	Err      string `json:"error,omitempty"`
}

type ClientInfo struct {
	ID         string `json:"id"`
	ConfigPath string `json:"config_path,omitempty"`
	HooksAware bool   `json:"hooks_aware"`
}

type TelemetryInfo struct {
	Enabled bool `json:"enabled"`
}

type LegacyInfo struct {
	Paths []string `json:"paths,omitempty"`
}

// Run produces a Report for the current state of the install. Every
// section is best-effort: errors in one section don't fail the whole
// doctor, they're recorded in the section's Err field.
func Run(ctx context.Context, recentLogLines int) Report {
	r := Report{}

	exePath, _ := os.Executable()
	r.Binary = BinaryInfo{
		Version:          version.Version,
		Commit:           version.Commit,
		BuildDate:        version.BuildDate,
		OSArch:           version.OSArch,
		GoVersion:        version.GoVersion,
		ProtocolVersions: version.ProtocolVersions,
		AEADAlgorithms:   version.AEADAlgorithms,
		Path:             exePath,
	}

	if state, err := service.Status(); err != nil {
		r.Service = ServiceInfo{State: state, Err: err.Error()}
	} else {
		r.Service = ServiceInfo{State: state}
	}

	port, portErr := ipc.CurrentPort()
	switch {
	case errors.Is(portErr, config.ErrConfigVersionFuture):
		r.Relay.ConfigError = portErr.Error()
	case errors.Is(portErr, config.ErrConfigCorrupt):
		r.Relay.ConfigError = portErr.Error()
	case port > 0:
		r.Relay.Port = port
		r.Relay.HealthOK, r.Relay.HealthDetail = probeHealth(ctx, port)
	}

	if tok, err := keychain.GetToken(); err == nil && tok != "" {
		r.Token.InKeychain = true
		ver, _ := config.ValidateToken(tok)
		r.Token.Format = ver
	}
	if id, err := keychain.GetInstallID(); err == nil {
		r.Token.InstallID = id
	}

	cwd, _ := os.Getwd()
	r.Project.CWD = cwd
	if dpath, err := config.WalkUpForDrift(cwd); err == nil {
		r.Project.DriftJSONPath = dpath
		if cfg, perr := config.ReadDrift(dpath); perr == nil {
			r.Project.Enabled = cfg.Enabled
			r.Project.Mode = cfg.Mode
		}
	}

	r.MCPConfig.Path = config.MCPPath()
	if mcp, err := config.ReadMCP(); err != nil {
		if _, statErr := os.Stat(r.MCPConfig.Path); statErr == nil {
			r.MCPConfig.Present = true
			r.MCPConfig.Err = err.Error()
		}
	} else {
		r.MCPConfig.Present = true
		r.MCPConfig.URL = mcp.BaseURL
	}

	for _, d := range clients.DetectAll() {
		r.Clients = append(r.Clients, ClientInfo{
			ID:         string(d.ID),
			ConfigPath: d.ConfigPath,
			HooksAware: d.HooksAware,
		})
	}

	r.Telemetry.Enabled = telemetry.Enabled()
	r.Legacy.Paths = migration.Detect().Paths

	if recentLogLines > 0 {
		if lines, err := driftlog.Tail(recentLogLines); err == nil {
			r.RecentLogs = lines
		}
	}

	return r
}

// FormatText renders Report as a human-readable terminal dump. Used
// when --json is NOT passed.
func FormatText(r Report) string {
	var sb strings.Builder
	sb.WriteString("drift doctor\n")
	sb.WriteString(strings.Repeat("=", 60))
	sb.WriteString("\n\n")

	sb.WriteString("binary\n")
	fmt.Fprintf(&sb, "  version:      %s (%s)\n", r.Binary.Version, r.Binary.Commit)
	fmt.Fprintf(&sb, "  build date:   %s\n", r.Binary.BuildDate)
	fmt.Fprintf(&sb, "  os/arch:      %s\n", r.Binary.OSArch)
	fmt.Fprintf(&sb, "  go:           %s\n", r.Binary.GoVersion)
	fmt.Fprintf(&sb, "  protocols:    %v\n", r.Binary.ProtocolVersions)
	fmt.Fprintf(&sb, "  algorithms:   %v\n", r.Binary.AEADAlgorithms)
	fmt.Fprintf(&sb, "  path:         %s\n\n", r.Binary.Path)

	sb.WriteString("service\n")
	fmt.Fprintf(&sb, "  state:        %s\n", r.Service.State)
	if r.Service.Err != "" {
		fmt.Fprintf(&sb, "  error:        %s\n", r.Service.Err)
	}
	sb.WriteString("\n")

	sb.WriteString("relay\n")
	switch {
	case r.Relay.ConfigError != "":
		// Distinguish "newer than this binary supports" (upgrade drift)
		// from a structurally-bad file (run drift install to repair).
		if strings.Contains(r.Relay.ConfigError, "schema") && strings.Contains(r.Relay.ConfigError, "newer") {
			fmt.Fprintf(&sb, "  port:         %s\n", r.Relay.ConfigError)
			sb.WriteString("                upgrade drift; do not delete the config\n")
		} else {
			fmt.Fprintf(&sb, "  port:         CONFIG CORRUPT: %s\n", r.Relay.ConfigError)
			sb.WriteString("                run 'drift install' to back up the bad file and rebuild fresh\n")
		}
	case r.Relay.Port > 0:
		fmt.Fprintf(&sb, "  port:         127.0.0.1:%d\n", r.Relay.Port)
		fmt.Fprintf(&sb, "  health:       %s\n", boolStr(r.Relay.HealthOK, "up", "down"))
		if r.Relay.HealthDetail != "" {
			fmt.Fprintf(&sb, "  detail:       %s\n", r.Relay.HealthDetail)
		}
	default:
		sb.WriteString("  port:         not set (run 'drift install')\n")
	}
	sb.WriteString("\n")

	sb.WriteString("token\n")
	fmt.Fprintf(&sb, "  in keychain:  %v\n", r.Token.InKeychain)
	if r.Token.Format != "" {
		fmt.Fprintf(&sb, "  format:       %s\n", r.Token.Format)
	}
	if r.Token.InstallID != "" {
		fmt.Fprintf(&sb, "  install_id:   %s\n", r.Token.InstallID)
	}
	sb.WriteString("\n")

	sb.WriteString("project\n")
	fmt.Fprintf(&sb, "  cwd:          %s\n", r.Project.CWD)
	if r.Project.DriftJSONPath != "" {
		fmt.Fprintf(&sb, "  .drift.json:  %s (enabled=%v, mode=%s)\n",
			r.Project.DriftJSONPath, r.Project.Enabled, r.Project.Mode)
	} else {
		sb.WriteString("  .drift.json:  not found (run 'drift init' to opt this project in)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("mcp config\n")
	fmt.Fprintf(&sb, "  path:         %s\n", r.MCPConfig.Path)
	fmt.Fprintf(&sb, "  present:      %v\n", r.MCPConfig.Present)
	if r.MCPConfig.URL != "" {
		fmt.Fprintf(&sb, "  url:          %s\n", r.MCPConfig.URL)
	}
	if r.MCPConfig.Err != "" {
		fmt.Fprintf(&sb, "  error:        %s\n", r.MCPConfig.Err)
	}
	sb.WriteString("\n")

	if len(r.Clients) > 0 {
		sb.WriteString("detected MCP clients\n")
		for _, c := range r.Clients {
			fmt.Fprintf(&sb, "  %-12s hooks-aware=%v config=%s\n", c.ID, c.HooksAware, c.ConfigPath)
		}
		sb.WriteString("\n")
	}

	fmt.Fprintf(&sb, "telemetry\n  enabled:      %v\n\n", r.Telemetry.Enabled)

	if len(r.Legacy.Paths) > 0 {
		sb.WriteString("legacy artifacts found\n")
		for _, p := range r.Legacy.Paths {
			fmt.Fprintf(&sb, "  - %s\n", p)
		}
		sb.WriteString("  (run 'drift install' to clean these up; --keep-legacy to preserve)\n\n")
	}

	if len(r.RecentLogs) > 0 {
		sb.WriteString("recent log lines\n")
		for _, line := range r.RecentLogs {
			sb.WriteString("  ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Paste this output to hello@driftlabs.io if you need help.\n")
	return sb.String()
}

// FormatJSON pretty-prints the Report as JSON.
func FormatJSON(r Report) string {
	out, _ := json.MarshalIndent(r, "", "  ")
	return string(out) + "\n"
}

// Write renders the report to w in the chosen format.
func Write(w io.Writer, r Report, asJSON bool) {
	if asJSON {
		_, _ = io.WriteString(w, FormatJSON(r))
		return
	}
	_, _ = io.WriteString(w, FormatText(r))
}

func probeHealth(ctx context.Context, port int) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, ""
	}
	return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// keep filepath imported for potential future per-OS detection
var _ = filepath.Join
