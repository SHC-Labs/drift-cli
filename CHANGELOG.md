# Changelog

All notable changes to drift get logged here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning follows [SemVer](https://semver.org/).

## [Unreleased]

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
- Token format with v1 discriminator (`drift_v1_*`), legacy `drift_<hex>` accepted as implicit v1
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
