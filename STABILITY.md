# Stability policy

What's part of the public contract (won't break in v2 without a migration window) vs what's internal (refactorable any time).

## Public contract

These shapes are stable. Breaking changes get the deprecation policy treatment: deprecated in v1.X, removed in v(X+3), customers get at least 3 minor versions of warning.

### Subcommand surface

The set of subcommands and their argument shapes:

```
drift install [--unsafe-mcp-url] [--keep-legacy]
drift uninstall
drift init
drift uninit
drift login
drift logout
drift status
drift doctor [--json]
drift relay status
drift relay logs [-n <lines>]
drift update
drift telemetry on
drift telemetry off
drift version [--json]
```

Hidden subcommands (`drift internal hook *`, `drift _service`) are NOT part of the public contract. They're invoked by hook scripts and the OS service manager respectively, and may change between versions.

### Hook protocol

Documented in detail in [ARCHITECTURE.md](ARCHITECTURE.md). Stable: the env vars read, the stdin format, the stdout `<drift-context>` block format, exit codes, the 5-second timeout budget.

### State event payloads

The four POST endpoints' request bodies (`/api/install/cli-installed`, `/api/install/client-connected`, `/api/install/relay-enabled`, `/api/install/relay-heartbeat`). Adding new fields is non-breaking; removing or renaming fields is breaking and gets the deprecation treatment.

See [DRIFT_INSTALL_STATE_API_SPEC.md](../DRIFT_INSTALL_STATE_API_SPEC.md) for the full contract.

### `~/.mcp.json` shape

The fields drift writes to `~/.mcp.json` are stable. Drift reads the rest of the file conservatively (modify only fields drift owns, never overwrite unrelated fields).

### Server endpoint URLs

Versioned via `/v1/` URL prefix. v2 endpoints land at `/v2/` without breaking v1 binaries already in the wild. The capability negotiation handshake lets the binary advertise what it supports and the server respond with what's available.

### Token format

`drift_v1_<payload>` going forward. Existing `drift_<hex>` tokens are parsed as implicit v1 for backwards compat. Future formats use a new version prefix (`drift_v2_*`).

### Logging format

JSON lines with `{"v": N, ...}`. Adding fields is non-breaking; removing or renaming requires a version bump.

## Internal (refactorable)

Everything in `internal/` that isn't covered above. Module layout, function signatures, package boundaries, struct fields, error messages (the codes are stable, the messages aren't), goroutine architecture, file paths inside `~/.drift/` (except the ones documented in PRIVACY.md).

## What "breaking" means

A change is breaking if a customer running an unmodified binary against a server with the change would behave differently in a way that loses functionality or corrupts state. Adding new optional fields, new subcommands, new flags with defaults that match the old behavior, or new server endpoints is not breaking.
