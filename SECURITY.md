# Security policy

## Reporting a vulnerability

Email `security@driftlabs.io` with the details. We respond within 48 hours.

GPG-encrypt sensitive reports with our public key (key fingerprint published on https://drift.io/security and in the binary's `drift doctor --json` output under `security_contact`).

Please do NOT file public GitHub issues for security vulnerabilities. The repo is open source; an unfixed vuln in a public issue puts customers at risk.

## Coordinated disclosure

We follow a 90-day coordinated disclosure window from the date of your report. We aim to ship a fix within 30 days for high-severity issues; the additional 60 days gives customers time to upgrade before the technical details land in the public CHANGELOG.

If we exceed 30 days without a fix, we'll proactively reach out with an explanation and a revised timeline.

## Scope

In scope:
- The drift binary and everything in this repository
- The release pipeline (GitHub Actions, cosign signing, SLSA provenance)
- The four `/api/install/*` endpoints the binary calls

Out of scope (file with the relevant team instead):
- Drift dashboard server (`mcp.driftlabs.io`): contact `security@driftlabs.io` with the same address but indicate "dashboard"
- Third-party MCP clients (Claude Code, Cursor, etc.)
- Third-party Go dependencies (file with upstream, then with us if it affects our threat model)

## What gets a bounty

We don't run a formal bounty program at v1. We will publicly acknowledge security researchers who follow responsible disclosure in `CHANGELOG.md` and in the release notes for the version that contains the fix.

## What doesn't qualify

- Self-XSS, social engineering, physical attacks
- Reports that require us to install vulnerable software you provide
- Reports against unsupported versions (we support the latest minor and the previous minor; older versions get a "please upgrade" response)
- Issues already disclosed in our `CHANGELOG.md`
