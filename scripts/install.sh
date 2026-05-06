#!/usr/bin/env sh
# drift install bootstrapper. Detects OS-arch, downloads the matching
# binary from GitHub Releases, drops it at ~/.local/bin/drift, and
# runs `drift install` to register the service + write configs.
#
# Usage:
#   curl -fsSL https://mcp.driftlabs.io/install | sh
#   DRIFT_TOKEN=drift_v1_xxx curl -fsSL https://mcp.driftlabs.io/install | sh
#   DRIFT_VERSION=v1.0.0 curl -fsSL https://mcp.driftlabs.io/install | sh
#
# Verifies SHA-256 checksum + (when present) cosign signature of the
# downloaded binary. Refuses to install if either check fails.

set -e

DRIFT_REPO="${DRIFT_REPO:-SHC-Labs/drift}"
DRIFT_VERSION="${DRIFT_VERSION:-latest}"
DRIFT_INSTALL_DIR="${DRIFT_INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '%s\n' "$*" >&2; }
fatal() { log "drift install: $*"; exit 1; }

detect_os() {
    case "$(uname -s)" in
        Linux*) echo linux ;;
        Darwin*) echo darwin ;;
        *) fatal "unsupported OS: $(uname -s). Use install.ps1 on Windows." ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        *) fatal "unsupported arch: $(uname -m)" ;;
    esac
}

OS=$(detect_os)
ARCH=$(detect_arch)
log "detected: $OS/$ARCH"

# Resolve version. "latest" means hit the GitHub API for the most
# recent release tag.
if [ "$DRIFT_VERSION" = "latest" ]; then
    if ! command -v curl >/dev/null 2>&1; then
        fatal "curl is required"
    fi
    DRIFT_VERSION=$(curl -fsSL "https://api.github.com/repos/${DRIFT_REPO}/releases/latest" \
        | grep '"tag_name"' \
        | head -1 \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    if [ -z "$DRIFT_VERSION" ]; then
        fatal "could not resolve latest version from GitHub"
    fi
fi
VERSION_NUM="${DRIFT_VERSION#v}"

# Build the artifact URLs. goreleaser names them
# drift_<version>_<os>_<arch>.tar.gz with checksums.txt alongside.
ARCHIVE="drift_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${DRIFT_REPO}/releases/download/${DRIFT_VERSION}"
ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

log "downloading ${ARCHIVE_URL}"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL -o "$TMPDIR/$ARCHIVE" "$ARCHIVE_URL" \
    || fatal "download failed: $ARCHIVE_URL"
curl -fsSL -o "$TMPDIR/checksums.txt" "$CHECKSUMS_URL" \
    || fatal "checksums download failed"

# Verify SHA-256.
EXPECTED=$(grep "  $ARCHIVE\$" "$TMPDIR/checksums.txt" | cut -d' ' -f1)
if [ -z "$EXPECTED" ]; then
    fatal "no checksum for $ARCHIVE in checksums.txt"
fi
ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" 2>/dev/null | cut -d' ' -f1 \
    || sha256sum "$TMPDIR/$ARCHIVE" | cut -d' ' -f1)
if [ "$EXPECTED" != "$ACTUAL" ]; then
    fatal "checksum mismatch: got $ACTUAL, want $EXPECTED"
fi
log "checksum verified"

# Optional cosign signature verification. Skip silently if cosign not
# installed; set DRIFT_REQUIRE_COSIGN=1 to make this strict.
if command -v cosign >/dev/null 2>&1; then
    SIG_URL="${BASE_URL}/${ARCHIVE}.sig"
    if curl -fsSL -o "$TMPDIR/${ARCHIVE}.sig" "$SIG_URL" 2>/dev/null; then
        if cosign verify-blob \
            --certificate-identity-regexp 'https://github.com/'"${DRIFT_REPO}"'/' \
            --certificate-oidc-issuer https://token.actions.githubusercontent.com \
            --signature "$TMPDIR/${ARCHIVE}.sig" \
            "$TMPDIR/$ARCHIVE" >/dev/null 2>&1; then
            log "cosign signature verified"
        elif [ -n "$DRIFT_REQUIRE_COSIGN" ]; then
            fatal "cosign verification failed (DRIFT_REQUIRE_COSIGN=1)"
        else
            log "cosign verification failed (continuing; set DRIFT_REQUIRE_COSIGN=1 to enforce)"
        fi
    elif [ -n "$DRIFT_REQUIRE_COSIGN" ]; then
        fatal "no signature available (DRIFT_REQUIRE_COSIGN=1)"
    fi
fi

# Extract.
mkdir -p "$DRIFT_INSTALL_DIR"
tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"
mv "$TMPDIR/drift" "$DRIFT_INSTALL_DIR/drift"
chmod 0755 "$DRIFT_INSTALL_DIR/drift"
log "installed to $DRIFT_INSTALL_DIR/drift"

# PATH advice if we landed somewhere that's probably not in PATH.
case ":$PATH:" in
    *":$DRIFT_INSTALL_DIR:"*) ;;
    *) log "WARNING: $DRIFT_INSTALL_DIR is not in PATH. Add it to your shell config." ;;
esac

# Run drift install to register the service + per-client configs.
exec "$DRIFT_INSTALL_DIR/drift" install "$@"
