package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DriftConfig is the parsed shape of a project's .drift.json. The bash hook
// pulls out enabled, denied tools, the report_edits flag, and the isolated
// mode flag. Schema-versioned: every persistent file has a top-level version
// field per the migration framework.
type DriftConfig struct {
	Version       int      `json:"version"`
	Enabled       bool     `json:"enabled"`
	Mode          string   `json:"mode,omitempty"`           // "default" | "isolated" | etc.
	DeniedTools   []string `json:"denied_tools,omitempty"`   // explicit tool deny list
	ReportEdits   bool     `json:"report_edits,omitempty"`   // PostToolUse opt-in (default true)
	ReportEditSet bool     `json:"-"`                        // tracks whether report_edits was set in JSON
	Path          string   `json:"-"`                        // absolute path the file was loaded from
}

// UnmarshalJSON is custom so we can detect whether report_edits was present
// in the source JSON. The shell `should-report` checked an explicit "false"
// vs absence; preserving that here for parity.
func (d *DriftConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["version"]; ok {
		_ = json.Unmarshal(v, &d.Version)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &d.Enabled)
	}
	if v, ok := raw["mode"]; ok {
		_ = json.Unmarshal(v, &d.Mode)
	}
	if v, ok := raw["denied_tools"]; ok {
		_ = json.Unmarshal(v, &d.DeniedTools)
	}
	if v, ok := raw["report_edits"]; ok {
		_ = json.Unmarshal(v, &d.ReportEdits)
		d.ReportEditSet = true
	} else {
		// Default-on: parity with the bash hook which treated absence as enabled.
		d.ReportEdits = true
	}
	return nil
}

// ReadDrift loads .drift.json from the given absolute path.
func ReadDrift(path string) (*DriftConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg DriftConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Path = path
	return &cfg, nil
}

// WalkUpForDrift walks from `start` toward filesystem root looking for
// .drift.json. Returns the absolute path of the first match, or
// ErrDriftConfigNotFound if the walk hits root without finding one.
//
// This mirrors the bash hook's walk_up_for_drift exactly, including the
// dirname == self stop condition that handles root and Windows drive roots.
func WalkUpForDrift(start string) (string, error) {
	if start == "" {
		return "", ErrDriftConfigNotFound
	}
	dir := filepath.Clean(start)
	for {
		candidate := filepath.Join(dir, ".drift.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrDriftConfigNotFound
		}
		dir = parent
	}
}

// ErrDriftConfigNotFound signals walk-up exhausted without finding .drift.json.
var ErrDriftConfigNotFound = errors.New(".drift.json not found")

// ShouldReport mirrors the bash drift-helpers.mjs `should-report` decision.
// Returns true only when the project opts in to PostToolUse reporting.
func (d *DriftConfig) ShouldReport() bool {
	if !d.Enabled {
		return false
	}
	if !d.ReportEdits {
		return false
	}
	if d.Mode == "isolated" {
		return false
	}
	for _, t := range d.DeniedTools {
		if t == "drift_broadcast_change" {
			return false
		}
	}
	return true
}
