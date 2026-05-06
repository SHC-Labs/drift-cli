// Package migration detects and cleans up legacy install artifacts:
//   - npm global @shadow-corp/drift-relay package + its daemon process
//   - ~/.drift/supervisor.ps1 and sentinel files
//   - Startup folder .bat shortcuts on Windows
//   - schtasks entries
//   - ~/.claude/hooks/drift-check.sh, drift-report.sh, drift-helpers.mjs
//   - ~/.claude/hooks/drift-*.bat Windows wrappers
//   - Old drift block in ~/.mcp.json pointing at the npm relay port
//
// Default behavior on drift install / drift update: migrate (uninstall old,
// install new). --keep-legacy preserves old install for A/B comparison.
//
// Backup before destructive write: copy old configs to
// ~/.drift/backups/<timestamp>/ before rewriting.
//
// Sprint 1 day 4-5 ships detection + logging only (no removal yet).
// Sprint 3 ships the cleanup.
package migration
