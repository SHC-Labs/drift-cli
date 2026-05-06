package migration

import (
	"os"
	"path/filepath"
)

// LegacyArtifacts is the result of scanning for pre-binary install
// artifacts left behind by the bash CLI + npm relay.
type LegacyArtifacts struct {
	Paths []string // absolute paths of legacy files that exist
}

// Found is shorthand for len(la.Paths) > 0.
func (la LegacyArtifacts) Found() bool { return len(la.Paths) > 0 }

// Detect scans the user's home for legacy artifacts. File-presence
// checks only; the cleanup logic in cleanup.go handles removal with
// backup-before-delete. The npm package + schtasks process detection
// is deferred to a follow-up since cleanup needs npm installed.
func Detect() LegacyArtifacts {
	home, err := os.UserHomeDir()
	if err != nil {
		return LegacyArtifacts{}
	}

	candidates := []string{
		// Bash hook scripts written by the old install
		filepath.Join(home, ".claude", "hooks", "drift-check.sh"),
		filepath.Join(home, ".claude", "hooks", "drift-report.sh"),
		filepath.Join(home, ".claude", "hooks", "drift-helpers.mjs"),
		// Windows .bat wrappers
		filepath.Join(home, ".claude", "hooks", "drift-check.bat"),
		filepath.Join(home, ".claude", "hooks", "drift-report.bat"),
		// PowerShell supervisor + sentinel from the 2026-05-04 .bat era
		filepath.Join(home, ".drift", "supervisor.ps1"),
		filepath.Join(home, ".drift", "supervisor.run"),
		filepath.Join(home, ".drift", "supervisor.log"),
	}

	var found []string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	return LegacyArtifacts{Paths: found}
}
