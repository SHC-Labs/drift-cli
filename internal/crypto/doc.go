// Package crypto holds the AEAD, ECDH, and HKDF wrappers used by the relay.
// All operations are interface-driven so v1.x can add new algorithms (e.g.
// ChaCha20-Poly1305) via the negotiation handshake without protocol breaks.
//
// v1 ships AES-GCM-256 only with random 96-bit nonces, byte-identical to the
// existing TS relay (drift-e2ee-v1: envelope). See the "Audit findings"
// subsection in DRIFT_BINARY_REWRITE_PLAN.md for the full contract: cipher
// choices, fixed HKDF info strings, key sizes, envelope shapes, and ECDH
// curve (P-256, NOT X25519).
package crypto
