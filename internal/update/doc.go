// Package update handles atomic self-update with cosign signature
// verification. Downloads new binary to drift.exe.new (Windows) or drift.new
// (Unix), verifies signature against the public key embedded in the current
// binary, atomic-renames, signals service to restart.
//
// Refuses unsigned updates. Closes the supply-chain attack vector on the
// release pipeline: even if GitHub Actions is compromised, a malicious
// release without a valid cosign signature is rejected by every binary in
// the wild.
package update
