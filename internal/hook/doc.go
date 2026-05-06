// Package hook implements the prompt-submit and post-tool-use handlers that
// the AI client invokes via "drift internal hook ...". Logic ports the
// existing bash hooks (drift-check.sh, drift-report.sh, post-commit-824488d).
//
// The protocol contract (env vars read, stdin format, stdout drift-context
// block, exit codes, timeout) is documented in ARCHITECTURE.md so future
// MCP clients can integrate cleanly.
package hook
