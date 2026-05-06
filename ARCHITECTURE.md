# Architecture

How the drift binary is structured. The public contract (what won't break in v2 without a migration window) lives in [STABILITY.md](STABILITY.md); the deprecation policy in [DEPRECATION.md](DEPRECATION.md).

## Module layout

```
drift/
├── cmd/drift/main.go        Entry point, calls cli.Execute()
└── internal/
    ├── cli/                 cobra subcommand handlers (one file per command)
    ├── relay/               local HTTP proxy + E2EE pipeline
    ├── crypto/              ECDH, AEAD, HKDF wrappers (interface-driven)
    ├── keychain/            OS keychain wrapper (zalando/go-keyring)
    ├── service/             systemd / launchd / Windows Service (kardianos)
    ├── hook/                prompt-submit + post-tool-use handlers
    ├── clients/             MCP client detection + per-client config writers
    ├── config/              .drift.json, ~/.mcp.json, ~/.drift/config.json + schema migrations
    ├── api/                 dashboard API client + OAuth login + capability handshake
    ├── update/              atomic self-update + cosign signature verification
    ├── ipc/                 local socket / named pipe / port abstraction
    ├── doctor/              diagnostics dump for support tickets
    ├── log/                 structured JSON log writer with rotation
    ├── telemetry/           opt-out kill switch + state event POSTs
    ├── migration/           legacy install detection + cleanup
    └── version/             build info + protocol versions + AEAD algorithms
```

## Public contract

Documented in detail in [STABILITY.md](STABILITY.md). High level: the public surface is the subcommand list, the hook protocol, the four state event payload shapes, the `~/.mcp.json` shape, and the `/v1/`-prefixed server endpoints. Everything else is internal and refactorable.

## Hook protocol

The AI client invokes hooks via `drift internal hook prompt-submit` (UserPromptSubmit event) and `drift internal hook post-tool-use` (PostToolUse event). Contract ported from the existing bash hooks (`drift-check.sh`, `drift-report.sh`, post-commit-824488d).

### Common contract (both hooks)

- Always exits 0. Success, silent skip, and loud-failure all return zero exit. Non-zero is reserved for genuine binary panic.
- Reads the relay URL from `~/.mcp.json`. The Bearer token lives in the OS keychain; the local relay handles upstream auth.
- Project gate: requires a `.drift.json` file with `enabled: true` somewhere up the directory tree from the search root.
- Honors `DRIFT_HOOK_SILENT=1` env to fall back to silent exit on every gate (back-compat for users who want the old quiet behavior).
- `~/.mcp.json` validation rejects empty token and the literal `YOUR_DRIFT_TOKEN` placeholder.

### `drift internal hook prompt-submit` (UserPromptSubmit)

Reads from env, writes to stdout.

**Env vars consumed:**
- `CLAUDE_PROJECT_DIR` (preferred) and `PWD`. Both walked up looking for `.drift.json` (the dual walk catches the case where Claude Code's workspace folder is a parent above the actual project root).
- `HOME` (for `~/.mcp.json` and `~/.cache/drift/state-hash`).

**HTTP calls:**
- Optional `PUT {BASE_URL}/api/projects/policy` (3s timeout) when `.drift.json` content hash differs from cached `/tmp/drift-policy-synced-<project_hash>`. Body built from the .drift.json + project_hash + content_hash. Response 2xx writes the new content_hash to the cache.
- `GET {BASE_URL}/api/check-updates?state_hash=<cached>` (3s timeout). HTTP code is captured separately so 401/403 (token rejected), 000/empty (network), and any other non-2xx all emit a loud `<drift-context>` block instead of silent failure.

**stdout format on success-with-content:**

```
<drift-context>
<server response body, minus the leading "DRIFT_HASH:..." line>

PROJECT IDENTIFIER -- when calling any drift_* tool from this project, include:
  project_hash: "<sha256-hex>"
The Drift server enforces this project's .drift.json policy using that hash.

PROJECT POLICY -- <path-to-.drift.json> marks this project with restrictions.   (only if DENIED_TOOLS set in .drift.json)
Tools that MUST NOT be called from this project: <comma-separated tool names>
Refuse the call if the user asks. Tell them this project's .drift.json denies it.

REQUIRED -- Do ALL of these steps in order:
1. Tell the user the Drift team status at the START of your response. Use ✅ when no conflicts, ⚠️ when there are conflicts.
2. BEFORE your first file edit, call drift_declare_intent with the files you plan to modify and a brief description. TELL THE USER you are declaring your intent.
3. <conflict-aware sentence: if "Active team work" appears in server content, ⚠️ stop-and-ask wording; otherwise ✅ proceed wording>

<task handling section if "📋.*task(s) assigned" appears in server content>
TASK HANDLING (when tasks are assigned):
a. If you were given a task, ASK the user first -- do NOT auto-claim. Say: 'You have a task assigned: [title]. Want me to start on it?'
b. If user agrees, call drift_claim_task with the task_id.
c. Follow the STOP-before-edit procedure on every file change (check_conflicts -> declare_intent -> edit -> broadcast_change).
d. When the work is complete, call drift_complete_task with status='completed' and a result summary listing what changed.
e. Do NOT git commit or git push. The task creator reviews your diff and ships it. Leaving files uncommitted is correct behavior.
</drift-context>
```

**stdout format on loud failure (`emit_inactive`):**

```
<drift-context>
Drift is INSTALLED but INACTIVE in this conversation.
Reason: <specific reason: missing config, placeholder token, no .drift.json, etc>

Action: tell the user the reason above so they can fix it.
</drift-context>
```

**State-hash diff cache:** `~/.cache/drift/state-hash` (one line, the latest hash from the server's response). Sent as `?state_hash=<cached>` on the next call so the server can short-circuit unchanged state.

**Policy-sync cache:** `/tmp/drift-policy-synced-<project_hash>` (one line, the SHA-256 of the .drift.json content last successfully synced to the server).

**Project hash derivation:** SHA-256 of either `git remote get-url origin` from the project dir (preferred) or the absolute project root path (fallback when there's no git remote).

**Timeout budget:** 3 seconds per HTTP call. Total wall-clock budget for the hook is bounded by the network calls plus filesystem walks, typically well under 5 seconds.

### `drift internal hook post-tool-use` (PostToolUse)

Reads from stdin, writes nothing (silent fire-and-forget).

**stdin format:** JSON payload from Claude Code's PostToolUse event. Includes `tool_input.file_path` for file-modifying tools.

**Logic:**
1. Parse stdin JSON, extract file_path. Empty/missing → exit 0.
2. Read `~/.mcp.json`. Missing token or URL → exit 0.
3. Walk up from `git rev-parse --show-toplevel` (or file dir if no git) looking for `.drift.json`. Not found → exit 0.
4. Check policy: skip if `enabled` is false, `report_edits` is false, `isolated` mode is set, or `drift_broadcast_change` is on the deny list.
5. Normalize file path: backslash → forward slash (Windows), case-insensitive prefix-strip if file path starts with REPO_ROOT (handles `C:/Users/...` vs `c:\\Users\\...` casing).
6. Compute project_hash same as drift-check.sh.
7. `POST {BASE_URL}/api/report-edit` with `{description: "Editing", files: [normalized], repo_url, project_hash}`. 2s timeout, runs detached, ignores the response.

**Timeout budget:** 2 seconds for the HTTP call. Hook returns immediately on stdin read, the HTTP call runs detached.

### Loud failure mode

The 2026-05-04 patch (commit 824488d) changed every silent-exit gate in `drift-check.sh` to an `emit_inactive` call that writes a `<drift-context>` block with the specific reason. Customers fixing fresh installs no longer see "drift just doesn't work"; they see "Drift is INSTALLED but INACTIVE in this conversation. Reason: ..." which the agent relays to them.

Set `DRIFT_HOOK_SILENT=1` to restore the old silent-exit behavior. This exists for back-compat; new installs should leave it unset.

`drift-report.sh` does NOT have loud-failure mode. It silently exits on every gate because PostToolUse fires after every tool use and a noisy hook would spam the activity feed. Reporting is best-effort.

## Crypto pipeline

v1 ships AES-GCM-256 with random 96-bit nonces, byte-identical to the existing TS relay. ECDH curve is P-256 (NOT X25519). HKDF-SHA256 with fixed info strings (`drift-kek-wrap-v1`, `drift-session:<YYYY-MM-DD>`, `drift-tag-v1`). HMAC-SHA256 fingerprints with the message `drift-relay:fingerprint`, truncated to 4 bytes hex.

Content envelope: `drift-e2ee-v1:` prefix + base64(JSON) where JSON keys are emitted in fixed order (`v, ct, nonce, tag, dek_id?, project_hash?`) for byte-identical TS interop. Inner `ct`, `nonce`, `tag` fields are base64 strings.

KEK wrap output: separate `wrapped_kek` / `nonce` / `tag` byte slices (server stores opaquely as base64). Key sizes: DEK 32B, KEK 32B, ECDH privkey 32B, ECDH pubkey 65B uncompressed SEC1 (`0x04 || X(32) || Y(32)`).

`internal/crypto/aead.go` defines an `AEAD` interface so future versions can add ChaCha20-Poly1305 via the algorithm negotiation handshake without a protocol break.

## Local IPC

The MCP client connects to the relay over HTTP at `127.0.0.1:<port>`. Port is randomly chosen at first `drift install` and persisted in `~/.drift/config.json`. Never changes after install. Hardened bind (SO_EXCLUSIVEADDRUSE on Windows, startup probe to detect leftover crashed-instance state, refuse to bind alternate ports on conflict).

v1.x adds Unix socket / Windows named pipe transports once MCP client ecosystem support catches up.

## Service installation

`internal/service` wraps `kardianos/service` so one Go API installs the binary as a systemd user unit on Linux, a launchd plist on macOS, or a Windows Service on Windows. User-scope installation, no elevation required.

## Self-update

`drift update` downloads the new binary to `drift.exe.new` (Windows) or `drift.new` (Unix), verifies the cosign signature against the public key embedded in the current binary, atomic-renames, signals the service to restart. Refuses unsigned or signature-invalid updates.

## Logging

JSON lines at `~/.drift/logs/drift.log`. Rotates at 10MB, keeps 5 generations. Format is versioned (`{"v": 1, "ts": ..., ...}`) so future binaries can extend without breaking log parsers.

## Configuration source layering

Lookup order:

1. Command-line flags (highest priority)
2. Environment variables (`DRIFT_*`)
3. Config file (`~/.drift/config.json`)
4. Defaults (lowest priority)

Adding a new config field in v2 just means a new field with a default; existing config files keep working unmodified.
