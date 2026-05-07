# Changelog

All notable changes to drift get logged here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning follows [SemVer](https://semver.org/).

## [Unreleased]

### Fixed (v0.1.3 hotfix)

- Token rejection error messages no longer echo the full token payload. Both the v1 and legacy charset errors now show a redacted fingerprint (`AAAA...XX`) and length only; the unknown-version error reports the version prefix (`v2x_`) without the payload that follows. Customers pasting install output or `drift doctor` results to a support inbox no longer leak their tokens (`internal/config/token.go`). Test added that fails if any rejection error contains a sentinel payload string.
- `drift install` auto-recovers from a corrupt `~/.drift/config.json`. Detects parse failure, renames the bad file to `config.json.corrupt.<unix>`, and rebuilds fresh. v0.1.2 printed the parse error mid-install and exited 0 with the relay port unset, leaving customers with a silently broken install (`internal/config/binary.go`, `internal/ipc/port.go`, `internal/cli/install.go`).
- `drift status` and `drift doctor` now distinguish a corrupt config from "never installed". The old "not set (run 'drift install')" message ran in both cases; now corrupt configs show "config corrupt at PATH (run 'drift install' to repair)" so customers don't waste time re-installing something that's already installed.
- Hook inactive-project message clarifies that the unreachable endpoint is the local relay, not the upstream Drift server. v0.1.2 said "could not reach Drift server at http://127.0.0.1:..." which read as if mcp.driftlabs.io was down. New message names the local relay explicitly and points at `drift status` to diagnose (`internal/hook/check.go`).
- `install.sh` and `install.ps1` pre-check `DRIFT_INSTALL_DIR`. If the path exists but is not a directory, fail with a useful error instead of letting `mkdir` print "Already exists" or PowerShell error generically.

### Fixed (v0.1.2 hotfix)

- Token validator now rejects mangled version prefixes like `drift_v2x_<payload>`, `drift_v2alpha_<payload>`, `drift_v123RC1_<payload>`. v0.1.1 broadened the legacy charset to base64url, which let those forms slip through `looksVersioned` when the payload was 16+ chars (`internal/config/token.go`). Tokens that legitimately start with `v` followed by a digit but have no underscore still parse as legacy.
- Hook inactive-project messages now point at the real command. v0.1.0 and v0.1.1 told customers to "Run 'drift project enable'" but no such command exists. Replaced with `drift init` for the missing-config case and an inline "set enabled=true or re-run drift init" message for the disabled case (`internal/hook/check.go`). Stale comment in `internal/cli/init.go` cleaned up at the same time.

### Fixed (v0.1.1 hotfix)

- Token validator accepts the actual dashboard charset (base64url: `A-Za-z0-9_-`) instead of hex-only. Previously rejected 100% of dashboard-issued tokens (`internal/config/token.go`, the `isHexLike` → `isTokenPayload` rename). Surfaced during internal team test of v0.1.0.
- Bash installer detects MINGW / MSYS / Cygwin (Git Bash on Windows) and prints the literal PowerShell one-liner copy-paste-ready instead of "use install.ps1 on Windows" with no instructions (`scripts/install.sh`).
- PowerShell installer auto-adds the install dir to User PATH (persistent) and refreshes `$env:PATH` in-session, replacing the easy-to-miss warning that left customers with `command not found` on first run (`scripts/install.ps1`).
- README, STABILITY, install scripts, and keychain comment now describe the real `drift_<base64url>` token format. Previously documented a `drift_v1_<hex>` format that was never issued.

### Added (v1 first push)

Single static Go binary replacing the bash CLI + npm relay + PowerShell supervisor + .bat hook wrappers.

Foundation:
- Cobra CLI with 14 subcommands (3 hidden: `_service`, `internal hook prompt-submit`, `internal hook post-tool-use`)
- Cross-compile to linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64
- Pure-Go (CGO_ENABLED=0), reproducible build flags (`-trimpath`, `-buildid=`, fixed `SOURCE_DATE_EPOCH`)
- Mandatory v1 docs published: README, LICENSE (MIT), PRIVACY, THREAT_MODEL, SECURITY, ARCHITECTURE, STABILITY, DEPRECATION, CHANGELOG, CONTRIBUTING

Hook handlers:
- `drift internal hook prompt-submit` ports `drift-check.sh` (post-commit-824488d)
- `drift internal hook post-tool-use` ports `drift-report.sh`
- Loud-failure mode: every gate emits a `<drift-context>` block; `DRIFT_HOOK_SILENT=1` restores silent-exit
- Dual walk-up from `$CLAUDE_PROJECT_DIR` and `$PWD` for `.drift.json`
- HTTP differentiation: 401/403/000/other on `/api/check-updates` each get a specific reason

Install pipeline:
- `drift install` writes `~/.mcp.json` pointing at the local relay (no Bearer header; relay handles upstream auth)
- Per-MCP-client detection + config writers for Claude Code, Cursor, Windsurf, Antigravity, Zed, Kimi, ChatGPT desktop, VS Code, Kilo
- LLM-agnostic: any MCP client speaking the protocol gets a working drift entry; auto-firing hooks for Claude Code, manual `drift_*` calls via `.cursorrules` / `AGENTS.md` for non-hooks-aware clients
- Migration cleanup with backup-before-delete to `~/.drift/backups/<timestamp>/`
- Service install via `kardianos/service` (systemd user unit, launchd, Windows Service)
- IPC port reserved at first install, persisted in `~/.drift/config.json`, hardened bind (SO_EXCLUSIVEADDRUSE on Windows)

Crypto + relay:
- AES-GCM-256 with random 96-bit nonces, byte-identical to TS relay
- ECDH P-256 (NOT X25519) + KEK wrap with HKDF-SHA256 and fixed info strings (`drift-kek-wrap-v1`, `drift-session:`, `drift-tag-v1`)
- `drift-e2ee-v1:` envelope encode/decode with controlled JSON field order for byte-identical TS interop
- 11 passing crypto tests + 14 passing token validation tests
- Token format: `drift_<base64url>` from the dashboard treated as implicit v1; explicit `drift_v1_*` accepted for forward-compat
- Capability negotiation handshake with 24h cached result
- Pubkey publishing flow, ECDH privkey persisted in OS keychain
- KEK + DEK + per-project DEK managers with cache + invalidate-on-mismatch
- Encryption pipeline integrated into relay request/response handlers (encrypts content fields, decrypts envelopes)
- `--disable-e2ee` option for staging environments without `/v1/relay/*` endpoints

Lifecycle commands:
- `drift login` with OAuth PKCE (S256 code challenge + state CSRF check)
- `drift update` with checksum + cosign signature verification
- `drift doctor` full diagnostics dump (text + `--json`)
- `drift telemetry on/off` persistent preference + `DRIFT_NO_TELEMETRY` env kill switch
- `drift status`, `drift relay status`, `drift relay logs`
- `drift init` / `drift uninit` per-project setup with idempotent re-runs
- `drift uninstall` walks back keychain + configs + service, idempotent

Observability:
- Structured JSON logging at `~/.drift/logs/drift.log`, 10MB rotation, 5 generations
- Heartbeat goroutine inside the service fires `relay-heartbeat` state event every 60s (server-overridable cadence)
- Four install state event POSTs to `/v1/install/*` with 5s/30s/120s retry

Distribution (partial; cert procurement blocked):
- goreleaser cross-compile config with cosign keyless signing + SBOM via syft
- Homebrew tap auto-publish + Winget manifest auto-submission
- GitHub Actions release workflow with OIDC + SLSA-3 provenance
- `install.sh` + `install.ps1` bootstrappers with checksum + cosign verification
- Reproducible builds verification job

### Pending (post-v1)
- Authenticode signing (Windows) and Apple notarization (macOS) when certs procured
- ChaCha20-Poly1305 added via the negotiation handshake (v1 ships AES-GCM only)
- Unix socket / named pipe IPC transport once MCP client ecosystem catches up
- APT, Snap, Chocolatey, AUR, Nixpkgs distribution
- Real cross-OS testing on clean Windows + macOS VMs
