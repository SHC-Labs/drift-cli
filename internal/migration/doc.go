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
// Detection + cleanup with backup-before-delete. drift install + drift
// update remove the artifacts; --keep-legacy preserves them for A/B
// comparison.
package migration
