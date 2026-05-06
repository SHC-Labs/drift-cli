# Changelog

All notable changes to drift get logged here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning follows [SemVer](https://semver.org/).

## [Unreleased]

### Added
- Sprint 1 day 1 scaffold: cobra CLI skeleton, full module layout, build info embedded, cross-compile to linux-amd64 / linux-arm64 / darwin-amd64 / darwin-arm64 / windows-amd64 verified
- `drift version` and `drift version --json` subcommands implemented
- Mandatory v1 docs: README, LICENSE (MIT), PRIVACY, THREAT_MODEL, SECURITY, ARCHITECTURE, STABILITY, DEPRECATION, CHANGELOG, CONTRIBUTING

### Pending (Sprint 1 day 2-3)
- `drift install` writes `~/.mcp.json`, registers hook shims, detects + configures Claude Code
- `drift internal hook prompt-submit` and `drift internal hook post-tool-use` ports of the bash hook logic
- Hook protocol contract documented in ARCHITECTURE.md

### Pending (Sprint 1 day 4-5)
- Local IPC: port-based with hardened bind
- Migration scaffolding: detect legacy installs, log to file (no removal yet)
- Mock state event POSTs for local dev

### Pending (Sprint 2)
- Crypto port (byte-identical to TS relay), relay HTTP proxy
- Service install via kardianos
- Keychain integration
- `drift init`, `drift uninit`, `drift relay status`, `drift relay logs`

### Pending (Sprint 3)
- Per-MCP-client detection + config writers (Cursor, Windsurf, Antigravity, VS Code, Zed, Kimi, ChatGPT, Kilo)
- `drift login` OAuth PKCE flow
- Auto-update with cosign signature verification
- Migration logic with `--keep-legacy` escape
- `drift doctor` diagnostics dump
- `drift telemetry on/off`
- `drift status`
- Four state event POSTs (live)

### Pending (Sprint 4)
- Cross-OS testing on clean Linux Docker, macOS runner, Windows runner
- Authenticode signing, Apple notarization
- Cosign + SLSA-3 attestation in CI
- Homebrew tap publish
- Winget manifest submission
- 30-line install.sh + install.ps1 at `mcp.driftlabs.io/install`
- URL pivot: `/install` serves new binary installer, `/install-bash` legacy bridge
