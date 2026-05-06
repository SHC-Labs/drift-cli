// Package ipc abstracts the local transport between the MCP client and the
// embedded relay. v1 ships port-based HTTP localhost (127.0.0.1:<port>)
// because MCP client ecosystem support for Unix sockets is sparse.
//
// Hardening per plan: random port at first install persisted in
// ~/.drift/config.json (never changes after install), SO_EXCLUSIVEADDRUSE on
// Windows, startup probe to detect leftover state from a crashed prior
// instance, refuse alternate ports on conflict (exit clean instead of
// silently picking 47822).
//
// v1.x adds Unix socket / named pipe transports once MCP client support is
// there; the negotiation handshake exposes which transports the binary
// supports.
package ipc
