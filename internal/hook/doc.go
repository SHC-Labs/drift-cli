// Package hook implements the prompt-submit and post-tool-use handlers that
// the AI client invokes via "drift internal hook ...". Logic ports the
// existing bash hooks (drift-check.sh, drift-report.sh, post-824488d).
//
// Sprint 1 day 2-3 fills this in. The resulting protocol contract (env vars
// read, stdin format, stdout drift-context block, exit codes, timeout) gets
// documented in ARCHITECTURE.md so future MCP clients can integrate cleanly.
package hook
