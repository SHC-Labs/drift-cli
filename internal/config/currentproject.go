package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CurrentProjectState is the breadcrumb the hook drops every time it
// fires for a project, so the relay can pick it up out-of-band and
// attach project_hash + project_name to drift_* MCP calls that didn't
// carry them in arguments. Closes the W0.6 bug class where a tool call
// with bare filenames collapsed to the wrong project_id on the server.
//
// File: ~/.drift/state/current-project.json, mode 0600. Single file
// (not per-project) because the relay is a single process serving one
// developer; the "current" project is whatever the hook last saw. A
// developer juggling two projects in two IDE windows will see the file
// flip every time the hook fires from either side, which is correct:
// the relay's next outbound MCP call should reflect the most recent
// hook activity.
type CurrentProjectState struct {
	Version     int       `json:"version"`
	ProjectHash string    `json:"project_hash"`
	ProjectName string    `json:"project_name"`
	ProjectDir  string    `json:"project_dir"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const currentProjectStateVersion = 1

// CurrentProjectStatePath returns ~/.drift/state/current-project.json,
// or empty string when HOME is not set. Mirror of BinaryConfigPath's
// empty-string-on-no-home contract.
func CurrentProjectStatePath() string {
	home, err := Home()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".drift", "state", "current-project.json")
}

// WriteCurrentProjectState writes the breadcrumb atomically. Best-effort:
// the hook should never fail because state I/O failed, so the caller
// swallows the error. Returned for tests + future doctor reporting.
func WriteCurrentProjectState(projectHash, projectDir string) error {
	path := CurrentProjectStatePath()
	if path == "" {
		return ErrHomeUnset
	}
	if projectHash == "" {
		return errors.New("project_hash is required")
	}
	state := CurrentProjectState{
		Version:     currentProjectStateVersion,
		ProjectHash: projectHash,
		ProjectName: filepath.Base(projectDir),
		ProjectDir:  projectDir,
		UpdatedAt:   time.Now().UTC(),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(path, data, 0o600)
}

// ReadCurrentProjectState reads the breadcrumb. Returns (nil, nil) when
// the file is absent (fresh install, or no hook fire yet); returns an
// error only on real I/O or parse failures. Callers (relay pipeline)
// treat (nil, nil) and any error as "no breadcrumb, forward unchanged".
func ReadCurrentProjectState() (*CurrentProjectState, error) {
	path := CurrentProjectStatePath()
	if path == "" {
		return nil, ErrHomeUnset
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var state CurrentProjectState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if state.ProjectHash == "" {
		return nil, nil
	}
	return &state, nil
}
