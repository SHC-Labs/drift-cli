#!/usr/bin/env bash
# CI cross-compile matrix. Builds the drift binary for every supported
# OS-arch in one pass, sanity-checks the outputs.
set -euxo pipefail

pwd
ls
go version
which go

LDFLAGS='-s -w -buildid='

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    os="${target%/*}"
    arch="${target#*/}"
    out="/tmp/drift-${os}-${arch}"
    echo "Building ${os}/${arch}"
    GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/drift
done

echo "All targets OK"
ls -la /tmp/drift-*
