# Contributing

drift is open source under MIT. PRs welcome from the community.

## Before you start

Read [DRIFT_BINARY_REWRITE_PLAN.md](../DRIFT_BINARY_REWRITE_PLAN.md). Every architectural decision has a reason; the plan documents the reasons. If you're tempted to argue with a decision, check the plan first; the conversation may already have happened.

## Build

```sh
go build ./cmd/drift
./drift version
```

Cross-compile (no CGO):

```sh
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w -buildid=" -o /tmp/drift-$target ./cmd/drift
done
```

## Test

```sh
go test ./...
```

## Lint

CI enforces `go vet`, `staticcheck`, and a custom grep that fails on shell injection patterns:

```sh
grep -rn 'exec.Command.*sh.*-c' . && exit 1 || true
```

If you write `exec.Command(name, args...)`, use direct args. Never `exec.Command("sh", "-c", "<interpolated>")`.

## Commit messages

Short imperative first line under 72 chars. Body explains why, not what (the diff shows what). No "Co-Authored-By: Claude" or other AI attribution. The user maintains ownership and credit.

Example:

```
crypto: add ChaCha20-Poly1305 to AEAD interface

Wires up the algorithm option in negotiation handshake without
shipping it client-side yet (server-side preference stays AES-GCM).
Sets up v1.x to start handing out ChaCha to capable customers.
```

## PR review

Smaller is better. PRs that touch one package + tests are easy to review and merge. PRs that touch five packages get nitpicked for weeks. If you have a big change, split it into a series of small PRs that each compile and pass tests on their own.

## Security

Don't file public issues for security vulnerabilities. See [SECURITY.md](SECURITY.md).
