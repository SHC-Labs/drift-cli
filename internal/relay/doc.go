// Package relay is the local HTTP proxy that sits between the customer's MCP
// client and the upstream Drift server. It handles per-request E2EE on the
// content fields, exposes a localhost port the MCP client connects to, and
// runs as a goroutine inside the service.
//
// Sprint 2 fills this in. v1 ships byte-identical to the existing TS relay
// (drift-e2ee-v1: envelope, AES-256-GCM with random 96-bit nonces, see
// DRIFT_BINARY_REWRITE_PLAN.md "Audit findings" for the full contract).
package relay
