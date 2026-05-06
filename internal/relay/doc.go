// Package relay is the local HTTP proxy that sits between the customer's MCP
// client and the upstream Drift server. It handles per-request E2EE on the
// content fields, exposes a localhost port the MCP client connects to, and
// runs as a goroutine inside the service.
//
// v1 ships byte-identical to the existing TS relay (drift-e2ee-v1: envelope,
// AES-GCM-256 with random 96-bit nonces). See ARCHITECTURE.md for the full
// crypto pipeline contract.
package relay
