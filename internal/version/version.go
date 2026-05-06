// Package version exposes build info embedded at link time and the protocol /
// algorithm versions the binary supports. Every bug report and telemetry event
// includes these so support can correlate behavior to a specific build.
package version

import "runtime"

// Build vars are injected via -ldflags at release time. Defaults below are
// what unbuilt or hand-built binaries report.
var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// ProtocolVersions are the URL-prefix protocol versions this binary speaks
// against the Drift server. Server can route requests to the right handler
// using the User-Agent header plus the URL prefix on each call.
var ProtocolVersions = []string{"v1"}

// AEADAlgorithms are the symmetric encryption algorithms this binary supports.
// v1 ships AES-GCM-256 only, matching the existing TS relay byte-for-byte.
// ChaCha20-Poly1305 is reserved for v1.x via the algorithm negotiation
// handshake, see DRIFT_BINARY_REWRITE_PLAN.md "Out of scope for v1".
var AEADAlgorithms = []string{"aes-gcm-256"}

// GoVersion is the runtime Go version, useful in bug reports.
var GoVersion = runtime.Version()

// OSArch is the OS/architecture pair, formatted like "linux/amd64".
var OSArch = runtime.GOOS + "/" + runtime.GOARCH
