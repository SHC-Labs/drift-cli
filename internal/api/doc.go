// Package api is the HTTP client for the Drift dashboard server. Includes
// the OAuth PKCE login flow, the capability negotiation handshake, and the
// four install state event POSTs (cli-installed, client-connected,
// relay-enabled, relay-heartbeat).
//
// Every request includes the User-Agent header (drift/<version> (os/arch))
// and hits a /v1/-prefixed URL. v2 endpoints land at /v2/ without breaking
// v1 binaries already in the wild.
package api
