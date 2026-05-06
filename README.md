# drift

Single static binary for the Drift coordination layer. Replaces the old bash CLI + npm relay daemon + PowerShell supervisor + .bat hook wrappers with one ~10MB executable that runs on Linux, macOS, and Windows.

## Status

Sprint 1 day 1 scaffold. Walking skeleton: compiles cross-OS, prints version, every subcommand registered, real implementations land Sprint 1-4. See [DRIFT_BINARY_REWRITE_PLAN.md](../DRIFT_BINARY_REWRITE_PLAN.md) for the full plan.

## Install

Not yet published. After Sprint 4 cutover:

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

## Build from source

```sh
go build -o drift ./cmd/drift
./drift version
```

Cross-compile (no CGO):

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o drift-linux-amd64 ./cmd/drift
```

Reproducible builds: every release pins `-trimpath`, `-ldflags="-s -w -buildid="`, and a fixed `SOURCE_DATE_EPOCH`. CI verifies two independent builds of the same commit produce byte-identical binaries.

## License

MIT, see [LICENSE](LICENSE).
