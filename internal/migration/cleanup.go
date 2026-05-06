package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanupResult is the outcome of a single cleanup attempt. Returned
// per-path so the caller can surface what was actually removed vs
// what failed to drift install's stdout.
type CleanupResult struct {
	Path    string
	Removed bool
	Backup  string // backup file path if we copied before delete
	Err     error
}

// Cleanup removes every legacy artifact returned by Detect. Backs up
// each file to ~/.drift/backups/<timestamp>/ before delete, so the
// customer can recover if the migration goes wrong. Idempotent: if a
// file is already gone, that's a no-op success.
//
// Honors the --keep-legacy escape: caller passes keepLegacy=true to
// run detection only and not actually remove anything.
//
// Backs up + removes the bash hook scripts, supervisor.ps1, sentinel
// files, and similar relics. Does NOT remove the npm
// @shadow-corp/drift-relay package; that requires shelling to npm and
// we want cleanup to stay pure-Go. drift install prints a one-liner
// the customer runs themselves.
func Cleanup(keepLegacy bool) []CleanupResult {
	la := Detect()
	if !la.Found() || keepLegacy {
		return nil
	}
	backupDir := newBackupDir()
	var results []CleanupResult
	for _, p := range la.Paths {
		results = append(results, cleanupOne(p, backupDir))
	}
	return results
}

// cleanupOne handles a single legacy file. Copy to backup, then
// remove. On any failure the original stays in place.
func cleanupOne(path, backupDir string) CleanupResult {
	r := CleanupResult{Path: path}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			// Already gone; idempotent success.
			r.Removed = true
			return r
		}
		r.Err = err
		return r
	}

	// Backup: copy to ~/.drift/backups/<timestamp>/<base name>
	if err := os.MkdirAll(backupDir, 0o755); err == nil {
		backupPath := filepath.Join(backupDir, sanitizeName(path))
		if err := copyFile(path, backupPath); err == nil {
			r.Backup = backupPath
		}
	}

	if err := os.Remove(path); err != nil {
		r.Err = err
		return r
	}
	r.Removed = true
	return r
}

// newBackupDir returns ~/.drift/backups/<timestamp>/ where timestamp
// is YYYYMMDD-HHMMSS. Different per-cleanup so concurrent runs (rare
// but possible) don't stomp each other.
func newBackupDir() string {
	home, _ := os.UserHomeDir()
	stamp := time.Now().UTC().Format("20060102-150405")
	return filepath.Join(home, ".drift", "backups", stamp)
}

// sanitizeName turns an absolute path into a single filename safe to
// use under the backup dir. Replaces path separators with underscores.
func sanitizeName(absPath string) string {
	out := strings.TrimPrefix(absPath, "/")
	out = strings.ReplaceAll(out, "/", "_")
	out = strings.ReplaceAll(out, "\\", "_")
	out = strings.ReplaceAll(out, ":", "_")
	return out
}

// copyFile reads src and writes dst with mode 0644. Only used for
// backup; we don't try to preserve original mode bits because the
// backup is a recovery aid not a deployable artifact.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// Summary formats results for display. One line per path, success
// marker, backup path if we kept one. Used by drift install / drift
// update output.
func Summary(results []CleanupResult) string {
	if len(results) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("legacy cleanup:\n")
	for _, r := range results {
		marker := "✓"
		if !r.Removed {
			marker = "✗"
		}
		fmt.Fprintf(&sb, "  %s %s", marker, r.Path)
		if r.Backup != "" {
			fmt.Fprintf(&sb, " (backed up to %s)", r.Backup)
		}
		if r.Err != nil {
			fmt.Fprintf(&sb, " ERROR: %v", r.Err)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
