// Package doctor produces the diagnostics dump for support tickets. Output
// includes binary version, server reachability, token validity, project
// status (cwd .drift.json walkup), per-client hook health, service status
// (kardianos.Status()), last 50 log lines from ~/.drift/logs/drift.log.
//
// Customers fix 80% of their own problems with this; the rest paste the
// output to support@driftlabs.io.
package doctor
