# drift

Single static binary for the Drift coordination layer. One ~10MB executable that runs on Linux, macOS, and Windows. Replaces the bash CLI, the npm relay daemon, the PowerShell supervisor, and the `.bat` hook wrappers with one artifact.

LLM-agnostic by design: any MCP client speaking the protocol gets a working drift entry. Auto-firing hooks for Claude Code; manual `drift_*` calls via `.cursorrules` or `AGENTS.md` for clients without auto-firing hook events. Cursor, Windsurf, Antigravity, Zed, Kimi, ChatGPT desktop, VS Code, and Kilo are all detected and configured automatically.

## Install

```sh
# Linux / macOS
curl -fsSL https://mcp.driftlabs.io/install | sh

# Windows
iwr https://mcp.driftlabs.io/install.ps1 | iex

# macOS via Homebrew
brew install SHC-Labs/drift/drift

# Windows via winget
winget install drift
```

Provide your token to skip the manual login step:

```sh
DRIFT_TOKEN=drift_v1_xxx curl -fsSL https://mcp.driftlabs.io/install | sh
```

Or run `drift login` after install to grab a token via the browser flow.

## Commands

```
drift install        Once-per-machine: register service, write mcp.json, set up hooks
drift uninstall      Reverse install: remove service + configs + keychain entries
drift init           Once-per-project: write .drift.json, register hook entries
drift uninit         Reverse init for the current project
drift login          OAuth PKCE flow, store token in OS keychain
drift logout         Clear keychain token
drift status         Brief health check
drift doctor         Verbose diagnostics dump (use --json for structured output)
drift relay status   Embedded relay state
drift relay logs     Tail recent relay logs
drift update         Atomic self-update (verifies cosign signature)
drift telemetry on   Opt into install state events
drift telemetry off  Opt out
drift version        Print version, OS-arch, protocol version, build date
```

`DRIFT_NO_TELEMETRY=1` in your env disables state events without persisting a preference.

## Build from source

```sh
go build -o drift ./cmd/drift
./drift version
```

Cross-compile (no CGO):

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w -buildid=" \
  -o drift-linux-amd64 ./cmd/drift
```

Reproducible builds: every release pins `-trimpath`, `-ldflags="-s -w -buildid="`, and a fixed `SOURCE_DATE_EPOCH`. CI verifies that two independent builds of the same commit produce byte-identical binaries.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the module layout, hook protocol contract, crypto pipeline, and configuration source order. [STABILITY.md](STABILITY.md) describes the public contract; [DEPRECATION.md](DEPRECATION.md) describes how breaking changes are handled.

## Privacy and security

[PRIVACY.md](PRIVACY.md) lists exactly what telemetry collects and how to opt out. [THREAT_MODEL.md](THREAT_MODEL.md) covers what the binary protects against and what it doesn't. Vulnerability disclosure: [SECURITY.md](SECURITY.md).

## License

MIT, see [LICENSE](LICENSE).
