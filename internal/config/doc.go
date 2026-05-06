// Package config reads and writes the persistent JSON files drift owns:
// ~/.drift/config.json (binary state), .drift.json (per-project enable),
// ~/.mcp.json (MCP client config drift wrote).
//
// Schema versioning: every persistent file has {"version": N, ...}. Migration
// framework reads version, applies migrations 1->2->3 sequentially. CI runs
// every migration path against fixtures.
//
// Atomic writes everywhere: write-to-tmp + rename. No partial writes, no
// corrupt state mid-edit.
//
// Source layering for runtime values: command-line flags > env vars
// (DRIFT_*) > config file > defaults.
package config
