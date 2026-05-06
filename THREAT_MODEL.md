# Threat model

What the drift binary protects against, what it doesn't, and what residual risks customers should know about.

## What we protect against

**Network adversaries between your machine and `mcp.driftlabs.io`.** All MCP content fields are end-to-end encrypted with AES-GCM-256 under a per-org DEK before they leave your machine. Server stores ciphertext only. TLS protects the metadata + envelope.

**Server compromise (limited blast radius).** Server holds wrapped KEKs and ciphertext, never the unwrapped DEK. A server-side breach exposes wrapped material; the attacker still needs the per-org KEK (held only in customer relay keystores) to decrypt anything.

**Supply chain on the release pipeline.** Every release artifact is cosign-signed via GitHub Actions OIDC. The auto-updater verifies signatures before swap. A compromised release pipeline cannot push malicious binaries to existing installs without a valid signature.

**Social engineering on the install URL.** `drift install` refuses to write `mcp_url` values outside `mcp.driftlabs.io` or `127.0.0.1:*`. Override requires both `--unsafe-mcp-url` and an interactive confirmation.

**Hook code injection.** Hook handlers never invoke `os/exec` with shell expansion. CI grep fails any commit that introduces `exec.Command(.*sh.*-c.*)`. No interpolated shell strings, ever.

## What we don't protect against

**Malicious code running as your user on the same machine.** OS keychains protect the Drift token from cross-user access but not from same-user processes. A malicious npm package, VS Code extension, or shell script running as you can read your Drift token from the keychain. This is the keychain's threat model, not a Drift-specific weakness.

**Localhost port hijack DoS.** If a malicious process on your machine binds the relay's persisted port before drift starts, drift exits cleanly with a clear error. This is a reliability vector (drift won't start), not a confidentiality vector (no traffic is intercepted).

**Compromised customer machine.** If your machine is fully compromised, an attacker with code execution as you can do anything you can do, including reading the keychain, exfiltrating local config, or spawning the relay with modified code.

**Server-side compromise of your dashboard account.** Drift cannot defend against an attacker who steals your dashboard login. Use a strong password, enable 2FA when available, rotate the API key from the dashboard if you suspect compromise.

## Residual risks worth naming

### Per-DEK message budget under random nonces

The relay uses random 96-bit AES-GCM nonces, matching the existing TS implementation byte-for-byte. NIST SP 800-38D headroom is 2^32 invocations per key (collision probability 2^-32). DEKs rotate on demand via `drift relay key rotate` from the dashboard.

Per-DEK budget at common volumes:

| Daily message volume | Time to 2^32 messages |
|----------------------|------------------------|
| 1,000 / day | ~12 million years |
| 100,000 / day | ~120,000 years |
| 1,000,000 / day | ~12 years |

Drift will add monitoring + automatic DEK rotation if any org's per-DEK volume approaches 2^30 (giving healthy 4x headroom below the 2^32 limit). Until then, the random-nonce strategy is documented here as the accepted residual risk.

### TLS certificate trust

The binary trusts the system root CA store for `mcp.driftlabs.io`. A compromised root CA could MITM the metadata layer (envelope, not content). The content layer's E2EE protects against this for actual message bodies but not for routing metadata. v1.x will add cert pinning as a defense-in-depth measure.

### Auto-update reaches out daily

The binary checks `https://mcp.driftlabs.io/v1/cli-version` once per day to learn about new releases. This is a network call from your machine. Disable with `DRIFT_NO_AUTO_UPDATE=1` if you prefer manual updates only.
