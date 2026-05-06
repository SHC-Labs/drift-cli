package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CurrentBinaryConfigVersion is the schema version this binary writes.
// Older versions get migrated up via Migrations on read; newer versions
// are an error (binary is too old to read this config).
const CurrentBinaryConfigVersion = 1

// BinaryConfig is the persistent state ~/.drift/config.json holds. Lives
// alongside the keychain entry (token + install_id + ECDH privkey).
//
// Schema-versioned per the plan's migration framework: every persistent
// JSON file has {"version": N, ...}. Adding new fields in v2 means a new
// migration step in Migrations; existing v1 configs auto-upgrade on read.
type BinaryConfig struct {
	Version    int    `json:"version"`
	RelayPort  int    `json:"relay_port,omitempty"`  // persisted random port, 0 if not yet chosen
	InstallID  string `json:"install_id,omitempty"`  // UUID, also mirrored in keychain
	Telemetry  string `json:"telemetry,omitempty"`   // "on" | "off" | "" (default on per PRIVACY.md)
}

// BinaryConfigPath returns ~/.drift/config.json. Pulled into a function
// so tests can override via $HOME.
func BinaryConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".drift", "config.json")
}

// ReadBinaryConfig loads ~/.drift/config.json, applying any pending
// schema migrations. Returns a zero-value config (with Version set to
// CurrentBinaryConfigVersion) when the file does not exist; that's the
// fresh-install case.
func ReadBinaryConfig() (*BinaryConfig, error) {
	path := BinaryConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &BinaryConfig{Version: CurrentBinaryConfigVersion}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	migrated, err := migrateBinaryConfig(data)
	if err != nil {
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	var cfg BinaryConfig
	if err := json.Unmarshal(migrated, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// WriteBinaryConfig persists cfg to ~/.drift/config.json atomically.
// Always writes the current schema version, preserving migration history.
func WriteBinaryConfig(cfg *BinaryConfig) error {
	cfg.Version = CurrentBinaryConfigVersion
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(BinaryConfigPath(), data, 0o600)
}

// migrateBinaryConfig is the migration framework entry point. Reads the
// raw bytes, peeks at the version, applies migrations 1->2->3->... until
// it matches CurrentBinaryConfigVersion. Returns the upgraded bytes ready
// for json.Unmarshal into BinaryConfig.
//
// v1 -> v2 + later migrations register here as the schema evolves.
func migrateBinaryConfig(data []byte) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	var version int
	if v, ok := raw["version"]; ok {
		_ = json.Unmarshal(v, &version)
	}
	if version == 0 {
		// Pre-versioned config (shouldn't exist for the binary, but
		// handle gracefully). Treat as v1.
		version = 1
	}
	if version > CurrentBinaryConfigVersion {
		return nil, fmt.Errorf("config version %d is newer than this binary supports (max %d). Upgrade drift", version, CurrentBinaryConfigVersion)
	}
	for version < CurrentBinaryConfigVersion {
		mig, ok := binaryMigrations[version]
		if !ok {
			return nil, fmt.Errorf("no migration registered for version %d -> %d", version, version+1)
		}
		newRaw, err := mig(raw)
		if err != nil {
			return nil, fmt.Errorf("migration %d -> %d: %w", version, version+1, err)
		}
		raw = newRaw
		version++
	}
	raw["version"] = json.RawMessage(fmt.Sprintf("%d", CurrentBinaryConfigVersion))
	return json.Marshal(raw)
}

// binaryMigrations maps fromVersion to a migration function that returns
// the upgraded raw map. Each migration handles exactly one version step;
// the framework chains them.
var binaryMigrations = map[int]func(map[string]json.RawMessage) (map[string]json.RawMessage, error){
	// Empty for v1 (current). Add entries as the schema evolves:
	//   1: func(raw map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	//       // v1 -> v2: e.g. rename a field, split a struct, etc.
	//       return raw, nil
	//   },
}
