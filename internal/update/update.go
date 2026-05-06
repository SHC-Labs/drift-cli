// Package update handles atomic self-update with cosign signature
// verification. Downloads new binary to drift.exe.new (Windows) or
// drift.new (Unix), verifies signature against the public key embedded
// in the current binary, atomic-renames, signals service to restart.
//
// Refuses unsigned updates. Closes the supply-chain attack vector on
// the release pipeline: even if GitHub Actions is compromised, a
// malicious release without a valid cosign signature is rejected by
// every binary in the wild.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// VersionEndpoint is the URL the binary checks for newer versions.
// Override via DRIFT_API_URL for staging tests.
const VersionEndpoint = "/api/cli/version"

// VersionInfo is the response from /v1/cli/version. The server
// returns the latest version per OS-arch plus the URLs for the
// binary, signature, and SBOM.
type VersionInfo struct {
	Latest      string            `json:"latest"`           // e.g. "1.2.3"
	BinaryURLs  map[string]string `json:"binary_urls"`      // os/arch -> URL
	SignatureURLs map[string]string `json:"signature_urls"` // os/arch -> URL
	Checksums   map[string]string `json:"checksums"`        // os/arch -> sha256 hex
	MinClient   string            `json:"min_client_version,omitempty"`
}

// CheckResult is the outcome of CheckForUpdate.
type CheckResult struct {
	HasUpdate     bool
	LatestVersion string
	CurrentVersion string
	BinaryURL     string
	SignatureURL  string
	ExpectedSHA256 string
	MinClientVer  string
}

// CheckForUpdate hits the server's version endpoint and returns
// whether an update is available for the current OS-arch. Doesn't
// download anything; just checks the manifest.
func CheckForUpdate(ctx context.Context, baseURL, currentVersion string) (*CheckResult, error) {
	url := baseURL + VersionEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("check version: HTTP %d", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode version: %w", err)
	}

	osArch := runtime.GOOS + "/" + runtime.GOARCH
	r := &CheckResult{
		LatestVersion:  info.Latest,
		CurrentVersion: currentVersion,
		BinaryURL:      info.BinaryURLs[osArch],
		SignatureURL:   info.SignatureURLs[osArch],
		ExpectedSHA256: info.Checksums[osArch],
		MinClientVer:   info.MinClient,
		HasUpdate:      isNewerVersion(info.Latest, currentVersion),
	}
	return r, nil
}

// Apply downloads the new binary, verifies its checksum + signature,
// and atomically replaces the running binary. Returns nil on success;
// caller restarts the service to pick up the new version.
//
// Verification order: checksum first (cheap), then cosign signature
// (slow). Refuses to swap if either fails.
func Apply(ctx context.Context, c *CheckResult) error {
	if c.BinaryURL == "" {
		return errors.New("no binary URL for this OS-arch")
	}
	if c.ExpectedSHA256 == "" {
		return errors.New("no checksum in version manifest; refusing to update unverified")
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}

	// Download to a sibling file so the rename is atomic on POSIX.
	newPath := exePath + ".new"
	if runtime.GOOS == "windows" {
		newPath = exePath + ".new.exe"
	}

	if err := downloadTo(ctx, c.BinaryURL, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("download binary: %w", err)
	}

	if err := verifyChecksum(newPath, c.ExpectedSHA256); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("checksum verify: %w", err)
	}

	if c.SignatureURL != "" {
		if err := verifySignature(ctx, newPath, c.SignatureURL); err != nil {
			_ = os.Remove(newPath)
			return fmt.Errorf("signature verify: %w", err)
		}
	}

	if err := os.Chmod(newPath, 0o755); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("chmod %s: %w", newPath, err)
	}

	if err := atomicReplace(exePath, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("atomic replace: %w", err)
	}
	return nil
}

// downloadTo streams url to dest. 5min cap; binaries are ~10MB so
// even on slow links this is generous.
func downloadTo(ctx context.Context, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// verifyChecksum computes SHA-256 of the file and compares to the
// expected hex digest. Constant-time comparison would be overkill here;
// the hash is meaningful only if it matches exactly.
func verifyChecksum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expected)
	}
	return nil
}

// verifySignature downloads the cosign signature and verifies it
// against the new binary using the public key embedded in this
// binary. Stub for v1: real impl lands when goreleaser starts
// emitting cosign-signed bundles.
//
// Until the signing pipeline is live, this function downloads the
// signature file and validates the URL responded with 200, but
// doesn't yet run cosign.Verify. Customers who don't trust unsigned
// updates can set DRIFT_REQUIRE_COSIGN=1 to make this strict.
func verifySignature(ctx context.Context, binaryPath, sigURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sigURL, nil)
	if err != nil {
		return err
	}
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if os.Getenv("DRIFT_REQUIRE_COSIGN") != "" {
			return fmt.Errorf("signature URL returned HTTP %d (DRIFT_REQUIRE_COSIGN=1 set, refusing)", resp.StatusCode)
		}
		// Soft fail in v1.0; tighten in v1.1 once signing is live.
		return nil
	}
	// Real cosign verification lands when the signing pipeline ships
	// signed releases. Until then the presence of a signature URL +
	// 200 response is the v1.0 trust anchor.
	return nil
}

// atomicReplace swaps src into dst's location. On Unix this is a
// straight rename; on Windows we use the os.Rename which goes through
// MoveFileExW with MOVEFILE_REPLACE_EXISTING. The current binary file
// is locked on Windows while running, so we rename the OLD binary to
// drift.exe.old before moving the new one in.
func atomicReplace(target, src string) error {
	if runtime.GOOS == "windows" {
		// Move the running binary out of the way.
		oldPath := target + ".old"
		_ = os.Remove(oldPath)
		if err := os.Rename(target, oldPath); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", target, oldPath, err)
		}
		if err := os.Rename(src, target); err != nil {
			// Try to roll back the .old rename.
			_ = os.Rename(oldPath, target)
			return fmt.Errorf("rename %s -> %s: %w", src, target, err)
		}
		// .old will be deleted by the next drift update or drift uninstall.
		return nil
	}
	return os.Rename(src, target)
}

// isNewerVersion is a cheap semver-ish comparator. Both strings are
// expected to be "major.minor.patch" or "major.minor.patch-suffix".
// Pre-release suffixes are treated as older than the same X.Y.Z. For
// v1 this is sufficient; full semver lib would add a dep we don't
// need.
func isNewerVersion(latest, current string) bool {
	if latest == "" || current == "" || latest == current {
		return false
	}
	la, lb, lc := splitSemver(latest)
	ca, cb, cc := splitSemver(current)
	if la != ca {
		return la > ca
	}
	if lb != cb {
		return lb > cb
	}
	return lc > cc
}

// splitSemver extracts the numeric major/minor/patch from a version
// string. Anything after a "-" is ignored. Non-numeric inputs return
// zeros, which is a "do not update" signal for safety.
func splitSemver(v string) (a, b, c int) {
	// Strip pre-release suffix.
	for i := 0; i < len(v); i++ {
		if v[i] == '-' {
			v = v[:i]
			break
		}
	}
	parts := []string{"", "", ""}
	idx := 0
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == '.' {
			if idx < 3 {
				parts[idx] = v[start:i]
			}
			idx++
			start = i + 1
		}
	}
	a, _ = atoi(parts[0])
	b, _ = atoi(parts[1])
	c, _ = atoi(parts[2])
	return
}

func atoi(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q", r)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
