# Privacy policy

What the drift binary collects, where it goes, how to opt out, how to delete.

## What is collected

The binary sends four install state events to the Drift dashboard server during normal use. Each event includes:

| Field | Source | Purpose |
|-------|--------|---------|
| `install_id` | Anonymous UUID generated at first `drift install` | Deduplicate state events per machine |
| `binary_version` | Build-time injected version string | Support tickets, deprecation tracking |
| `os` | `runtime.GOOS` | One of `linux`, `darwin`, `windows` |
| `arch` | `runtime.GOARCH` | One of `amd64`, `arm64` |
| `hostname_hash` | SHA-256 of hostname, first 16 hex chars (optional) | Distinguish multi-machine installs by the same developer |
| `client` (per detected MCP client) | Filesystem detection of known config paths | Drives the dashboard's "Connect a client" checkoff |
| `transport`, `port_or_path` | Relay binding result | Drives the dashboard's "Enable E2EE" status |
| `uptime_seconds` (heartbeat, every 60s) | Process clock | Powers the dashboard's relay health widget |

Plus the standard HTTP request metadata: User-Agent header (`drift/<version> (os/arch)`) and the source IP (visible to the server regardless of opt-in / opt-out).

## What is NOT collected

The binary never sends:

- File paths
- Project names
- Source code
- Prompts
- Hostname (only the SHA-256 hash, optionally)
- Anything inside `~/.drift/logs/drift.log` (logs stay local)
- Anything from your MCP traffic (the relay encrypts content end-to-end before it leaves your machine)

## Where it goes

The four install state events POST to `https://mcp.driftlabs.io/api/install/*` over TLS. Persisted in the Drift Postgres database under the `installs` and `client_connections` tables. Per-org row-level security enforced; rows are visible only to your org.

Telemetry beyond the install state events does not exist in v1.

## Opt out

Three equivalent ways:

```sh
# Env var (per-process)
DRIFT_NO_TELEMETRY=1 drift status

# Subcommand (persists in ~/.drift/config.json)
drift telemetry off

# Re-enable later
drift telemetry on
```

When opted out, the binary skips all four install state event POSTs.

## Data deletion

Email `support@driftlabs.io` with your `install_id` (find it via `drift doctor --json`). Server deletes the corresponding rows from `installs` and `client_connections` within 30 days. Cascade delete also runs when your org is closed.

## Changes to this policy

Material changes get announced in `CHANGELOG.md` under a `Privacy` heading and via a banner in the dashboard. The binary refuses to enable new telemetry collection categories without an explicit version bump and a customer-visible notice.
