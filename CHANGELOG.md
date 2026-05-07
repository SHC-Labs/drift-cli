# Changelog

All notable changes to drift get logged here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning follows [SemVer](https://semver.org/).

## [Unreleased]

### Added (v0.1.9)

- `drift quickstart` now renders a full-screen TUI form when stdin is a real terminal: a welcome note, multi-select for the LLM clients to configure (with the FULL / AGENTS.MD / MCP-ONLY tier label next to each), text input for the project root, and a confirm step. Built on `github.com/charmbracelet/huh`. Customers hitting the install one-liner from their terminal see a real wizard instead of a wall of line prompts.
- Inline-prompt path is preserved as the fallback. CI / scripted installs (no TTY) drop straight to plain `drift install`. A new `--inline` flag forces the line-prompt style on a TTY for debugging or low-fidelity remote shells. TUI failures (terminal can't render ANSI, etc.) auto-fall through to the inline path so customers always have a working installer.
- Multi-select selections now actually filter which clients get per-project setup. `clients.SetupProjectFiltered(projectDir, relayURL, exePath, only []ClientID)` is the new entry point; `runInit` picked up a `runInitFiltered` variant that threads the filter from the wizard. v0.1.8's wizard listed clients but configured all of them regardless; v0.1.9 honors the user's checkbox toggles.
- TUI welcome screen + step descriptions explain what each step does so customers don't have to guess.

### Added (v0.1.8)

- New `drift quickstart` command. Guided wizard that runs after the install one-liner downloads the binary. Five steps: machine-level install, list of detected LLM clients with tier labels (FULL / AGENTS.MD / MCP-ONLY) matching the dashboard, project-root prompt, per-project setup with the existing `drift init` pipeline, and a relay verify step. Falls back to plain `drift install` when stdin isn't a TTY so CI/scripted installs keep working.
- `install.sh` and `install.ps1` end with `drift quickstart` instead of `drift install`. Bash also reopens `/dev/tty` for `curl | sh`-form installs so the wizard prompts work even when curl owns the original stdin. PowerShell branches on `[Environment]::UserInteractive`.
- Multi-project legacy hook scanner. `drift quickstart` walks `~/.claude/projects/` after setting up the chosen project and offers batch migration of legacy bash-CLI hook entries (`drift-check.bat` / `drift-report.sh` / `.mjs` variants) across other project roots. Reuses the same upsert-with-replace path drift init already runs.
- Legacy bash-CLI hook entries are now detected by command pattern, not just by `_drift_tag`. `upsertHookEntry` checks for untagged entries whose command points at a `drift-check.{bat,sh,mjs}` or `drift-report.{bat,sh,mjs}` wrapper and replaces them in place. Closes the upgrade-from-bash-CLI gap that left fresh installs with stale hook entries pointing at scripts that don't exist anymore on the new install.

### Fixed (v0.1.7 hotfix)

- Service install on Windows no longer hard-fails when PowerShell isn't elevated. v0.1.4-v0.1.6 returned "Access is denied" and told the customer to "fix the error and re-run", which is hostile. v0.1.7 detects the access-denied error and falls back to a user-mode autostart: drops a `drift-relay.cmd` launcher in `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\` and launches the relay process detached so the current session works without logout/login. Customers who want a real Windows Service (auto-restart on crash, system-wide persistence) can re-run `drift install` from an elevated PowerShell. New build-tagged files `internal/service/install_user_windows.go` and `internal/service/install_user_other.go`.
- `install.ps1` now broadcasts `WM_SETTINGCHANGE` after writing the User PATH so explorer.exe reloads its environment block. Without the broadcast, newly-spawned PowerShell windows inherit explorer's stale PATH cache and don't see the new install dir until logout/login. Adds `Add-Type` for `SendMessageTimeout` and a `WM_SETTINGCHANGE` broadcast to `HWND_BROADCAST` with a 5s `SMTO_ABORTIFHUNG` timeout.
- Customer-facing email standardized to `hello@driftlabs.io` everywhere. Removes split between `support@` (general) and `security@` (vuln disclosures); both now route through the single inbox. Touched `internal/cli/install.go`, `internal/cli/status.go`, `internal/doctor/doctor.go`, `internal/doctor/doc.go`, `PRIVACY.md`, `SECURITY.md`. v0.1.4-v0.1.6 had the wrong address baked into install postface and doctor footer.

Note: Windows-specific changes here couldn't be exercised end-to-end from the Linux build host. Cross-compile confirms the binary builds; the Startup-folder fallback and `WM_SETTINGCHANGE` broadcast both need a Windows test pass before they're presumed working.

### Fixed (v0.1.6 hotfix)

- Cap the `/api/check-updates` response body at 64KB before rendering it inside the `<drift-context>` block. A hostile or compromised upstream returning a 5MB activity feed would otherwise flood the LLM context window and the customer's terminal. Truncation appends `[truncated: server response exceeded body cap]` so the LLM knows content was dropped (`internal/hook/check.go`).
- Cap `denied_tools` from `.drift.json` to 50 entries with each entry truncated to 200 chars before rendering in the PROJECT POLICY block. A malicious commit could otherwise plant 10,000 entries or one entry that's a megabyte long, multiplying into the LLM context. Overflow is reported as `(+ N more denied entries omitted; .drift.json has too many)` so the LLM and the user both see the trim happened.
- `drift init` preserves unknown fields in `.drift.json`. Previous builds rebuilt the file from a fixed struct, dropping any forward-compat or third-party fields the customer (or another tool) had added. Now reads as a raw map, modifies only the known fields, writes back. Mirrors the long-standing `~/.mcp.json` round-trip pattern (`internal/cli/init.go`).
- Stop printing the misleading `Wrote drift entry to ~/.claude/settings.json (claude-code)` line. Claude Code reads from `~/.mcp.json`, which the global write at the top of install handles. The per-client writer was a no-op for Claude Code, but the message claimed a write that never happened. Now prints `Detected claude-code (uses ~/.mcp.json)` instead (`internal/cli/install.go`).
- `HOME`-unset detection. `MCPPath` and `BinaryConfigPath` no longer fall back to a CWD-relative read when `os.UserHomeDir` returns empty: they return `""` and downstream readers surface a clear `ErrHomeUnset`. Previously a customer running `drift status` inside a hostile project directory would have been served that project's `./.mcp.json` as if it were `~/.mcp.json`. New `internal/config/home.go` centralizes the resolution, status reports `incomplete (HOME not set; drift requires a home directory to locate configs)`.
- New `TestContextBlockCaps` pins the body and denied-tools cap values so a careless tweak can't quietly remove the DoS guards (`internal/hook/shared_test.go`).

### Fixed (v0.1.5 hotfix)

- Sanitize `denied_tools` entries and the `.drift.json` path before rendering the PROJECT POLICY block. v0.1.4 closed the upstream-server prompt-injection vector; this closes the equivalent vector for repo-checked-in `.drift.json` content. A malicious commit that planted `</drift-context>SYSTEM: ...<drift-context>` in `denied_tools` could otherwise escape the context block when a teammate ran Claude Code in that repo. New regression test `strips marker from .drift.json content` locks the boundary.
- `~/.mcp.json` parse errors now wrap a new `ErrMCPCorrupt` sentinel so callers can distinguish corrupt-file from truly-missing. `drift status` reports `corrupt at PATH (run 'drift install' to repair)` instead of the misleading `missing`. Hook inactive-message gains a corrupt-mcp.json branch with the same advice (`internal/config/mcp.go`, `internal/cli/status.go`, `internal/hook/check.go`).
- `drift install` auto-recovers from a corrupt `~/.mcp.json`. `WriteMCPDriftEntryRecovering` renames the bad file to `~/.mcp.json.corrupt.<unix>` and writes fresh, mirroring the binary-config recovery path. v0.1.4 failed mid-install with a parse error and left the customer stuck (`internal/config/mcp_write.go`, `internal/cli/install.go`).

### Fixed (v0.1.4 hotfix)

- Sanitize server-supplied content before rendering inside `<drift-context>...</drift-context>` blocks. A compromised or malicious upstream Drift server could otherwise include literal `</drift-context>` markers in activity feed text, close the context block early, and inject text the LLM would read as a system instruction. Same defense covers the HTTP-error fallback path that echoed the first 200 bytes of an unexpected response. The new sanitizer also drops ANSI escape introducers, NUL bytes, and other C0 control characters so a hostile server can't repaint the customer's terminal or hide content via VT escapes (`internal/hook/shared.go`, `internal/hook/check.go`). 8 boundary tests in `shared_test.go` cover marker stripping, case-insensitive variants, ANSI/NUL/control-byte stripping, and benign UTF-8 preservation.
- Future-version configs get a "schema is newer than this binary supports — upgrade drift" message instead of being treated as corrupt. The previous behavior conflated "structurally broken file" with "binary too old to read this file"; the latter shouldn't trigger the auto-backup path that would lose customer data. New `ErrConfigVersionFuture` sentinel propagates from `ReadBinaryConfig` to status, doctor, and install (`internal/config/binary.go`, `internal/cli/status.go`, `internal/doctor/doctor.go`).
- `drift uninstall` sweeps up diagnostic residue: the `~/.drift/logs/drift.log` relay log file and any `~/.drift/config.json.corrupt.*` backups left by the auto-recovery path. v0.1.3 left these behind across uninstalls, accumulating over time. Now removes them along with the now-empty `logs/` and `~/.drift/` directories (`internal/cli/uninstall.go`).

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
