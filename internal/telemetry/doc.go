// Package telemetry stub: kill switch + opt-out machinery from day one even
// though v1 only emits the four install state events. DRIFT_NO_TELEMETRY=1
// env var disables everything; "drift telemetry off" subcommand writes the
// opt-out to config.
//
// PRIVACY.md documents the exact fields collected: install_id (anonymous
// UUID), version, OS-arch, install success/failure, state events. NEVER:
// file paths, project names, code, prompts, hostname (only hash), IP.
//
// Telemetry destination is the dashboard-api /telemetry endpoint (separate
// from the four state event POSTs). v1 sends nothing here but plumbing
// exists so v1.x can opt customers in without a binary update.
package telemetry
