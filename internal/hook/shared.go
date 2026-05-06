package hook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SilentEnv is the env var honored by drift-check. Set to non-empty to
// restore the pre-824488d silent-exit behavior (no <drift-context> on
// gates). Default is loud-failure mode.
const SilentEnv = "DRIFT_HOOK_SILENT"

// EmitInactive writes the "Drift INACTIVE" context block to w with the
// given reason and returns. Caller exits 0 after this. Honors SilentEnv:
// when set, writes nothing.
//
// Mirrors the bash emit_inactive helper added in 824488d.
func EmitInactive(w io.Writer, reason string) {
	if os.Getenv(SilentEnv) != "" {
		return
	}
	fmt.Fprintln(w, "<drift-context>")
	fmt.Fprintln(w, "Drift is INSTALLED but INACTIVE in this conversation.")
	fmt.Fprintf(w, "Reason: %s\n", reason)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Action: tell the user the reason above so they can fix it.")
	fmt.Fprintln(w, "</drift-context>")
}

// ProjectHash computes the SHA-256 hash the server uses to identify a
// Drift project. Prefers the canonical git remote URL from `git remote
// get-url origin`; falls back to the absolute project root path when the
// project has no git remote.
//
// The hash input is the raw string with no separator or trailing newline,
// matching the bash hook's `printf '%s'` semantics.
func ProjectHash(projectDir string) string {
	input := gitRemoteOrigin(projectDir)
	if input == "" {
		input = projectDir
	}
	return Sha256Hex(input)
}

// Sha256Hex returns the lowercase hex SHA-256 of the input string. Used
// for project_hash and content_hash derivation; both must match the
// bash hook's `printf '%s' | sha256sum | cut -d' ' -f1` output exactly.
func Sha256Hex(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// gitRemoteOrigin runs `git remote get-url origin` in the given directory.
// Returns empty string on any error (no git, not a repo, no origin remote);
// callers fall back to the project dir path.
func gitRemoteOrigin(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitTopLevel runs `git rev-parse --show-toplevel` in the given directory.
// Returns empty string on any error. Used by the report hook to find the
// repo root before walking up for .drift.json.
func gitTopLevel(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NormalizeFilePath ports drift-helpers.mjs `normalize-files`. Two
// transformations:
//
//  1. Backslash to forward slash. Some IDEs pass Windows paths with `\\`
//     while git's output uses `/`. Forward-slash output also dodges the
//     Cloudflare WAF rule that 400-rejects backslash-laden JSON bodies.
//
//  2. Case-insensitive prefix-strip when the file path starts with the
//     repo root path. `git rev-parse` may emit `C:/Users/...` while the
//     IDE may pass `c:\\Users\\...`; we want a clean repo-relative path
//     either way.
//
// Returns a slice with one element (the normalized file path). Returning
// a slice keeps the caller's JSON shape `["path"]` consistent with the
// bash hook's output.
func NormalizeFilePath(filePath, repoRoot string) []string {
	norm := strings.ReplaceAll(filePath, "\\", "/")
	if repoRoot != "" {
		root := strings.ReplaceAll(repoRoot, "\\", "/")
		root = strings.TrimSuffix(root, "/")
		// Case-insensitive prefix check.
		if len(norm) > len(root) && strings.EqualFold(norm[:len(root)], root) {
			rest := norm[len(root):]
			if strings.HasPrefix(rest, "/") {
				norm = strings.TrimPrefix(rest, "/")
			}
		}
	}
	return []string{norm}
}

// CacheDir returns ~/.cache/drift/, creating it if necessary. Used for the
// state-hash cache file.
func CacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".cache", "drift")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
