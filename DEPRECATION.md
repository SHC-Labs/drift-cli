# Deprecation policy

How drift handles breaking changes to the public contract.

## The rule

Features marked deprecated in `v1.X` are removed in `v(X+3)`. Customers get a minimum 3-version window of "still works but warns".

Example timeline:

```
v1.5    Feature X marked deprecated. Still works.
v1.6    Still works, prints warning on every use.
v1.7    Still works, prints warning on every use.
v1.8    Feature X removed.
```

The warning surface lives in `internal/version.WarnDeprecated()`:

```go
func WarnDeprecated(feature, removedIn string) {
    if telemetry.Enabled() {
        telemetry.Send("deprecated_use", map[string]any{
            "feature": feature,
            "removed_in": removedIn,
        })
    }
    fmt.Fprintf(os.Stderr, "warning: %s is deprecated, removed in %s\n", feature, removedIn)
}
```

Telemetry tells us which deprecated features are still in use across the install base before we ship the version that removes them. If a deprecation has high active use 2 minor versions in, we extend the window or rethink the removal.

## What gets the deprecation policy

Anything in [STABILITY.md](STABILITY.md)'s "Public contract" section: subcommand surface, hook protocol, state event payloads, `~/.mcp.json` shape, server endpoint URLs, token format, logging format.

## What doesn't

Internal-only changes (anything in `internal/` not covered by the public contract) can change between any two versions without a deprecation window. Breaking changes to internals get a `CHANGELOG.md` note and may surprise people running pre-release builds, but customers running stable releases never see them.

## Force-deprecation

The capability negotiation handshake includes a `min_client_version` field. If the server returns a version higher than what the binary reports in its User-Agent, the binary refuses to proceed and prints an upgrade message.

This exists for emergency cases only (security vulnerability, data corruption bug) where running an old version is actively harmful. The 3-version deprecation window does NOT apply when `min_client_version` is bumped; the only mitigation is upgrade. Use sparingly.

## Sunset announcements

A version's deprecation list is published in:

- `CHANGELOG.md` under a `Deprecated` heading
- The dashboard's release notes page
- Email to active orgs (one-time, when deprecation lands and again 30 days before removal)
