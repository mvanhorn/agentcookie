#!/usr/bin/env bash
#
# release-tarball.sh - Build the closed-beta release tarball that goes
# into a GitHub release. Bundles the notarized agentcookie binary with
# the install-beta.sh script and the closed-beta quickstart guide. The
# install script knows how to consume this exact shape.
#
# Usage:
#   scripts/release-tarball.sh <version>
#
# Where <version> matches the release tag (e.g. v0.12.0-beta.1). The
# script produces:
#
#   dist/agentcookie-<version>-darwin-universal.tar.gz   (arm64 + x86_64)
#
# When bin/agentcookie is a single-arch binary (e.g. a `make build` dev
# build rather than `make build-universal`), the tarball is named for that
# lone arch instead (darwin-arm64 / darwin-amd64).
#
# Prereqs:
#   1. bin/agentcookie exists, signed and notarized (run `make release`
#      first; this script does not re-invoke notarization).
#   2. scripts/install-beta.sh and docs/quickstart-beta.md are present
#      in the repo.
#
# This script is intentionally not part of `make release`. CI runs
# `make release` first, then this script. Local releases can do the
# same sequence.

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/release-tarball.sh <version>" >&2
  exit 1
fi
VERSION="$1"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

BIN="bin/agentcookie"
INSTALL_SCRIPT="scripts/install-beta.sh"
QUICKSTART="docs/quickstart-beta.md"

for path in "$BIN" "$INSTALL_SCRIPT" "$QUICKSTART"; do
  if [[ ! -f "$path" ]]; then
    echo "release-tarball.sh: missing required file: $path" >&2
    echo "release-tarball.sh: run 'make release' first to produce bin/agentcookie" >&2
    exit 2
  fi
done

# Verify the binary is signed (notarization status is harder to verify
# offline; we leave that to spctl at install time on the consumer Mac).
if ! codesign -d -r- "$BIN" >/dev/null 2>&1; then
  echo "release-tarball.sh: bin/agentcookie has no codesign signature" >&2
  echo "release-tarball.sh: run 'make sign' (or 'make release' for full pipeline)" >&2
  exit 2
fi

# Name the tarball for the binary's actual architecture(s). A Universal 2
# binary (make build-universal) carries both slices and ships as
# "darwin-universal"; a single-arch dev build ships as darwin-amd64 /
# darwin-arm64. lipo -archs reports "x86_64" (Go's amd64) and/or "arm64".
BIN_ARCHS="$(lipo -archs "$BIN" 2>/dev/null || true)"
if [[ "$BIN_ARCHS" == *arm64* && "$BIN_ARCHS" == *x86_64* ]]; then
  TARBALL_ARCH="darwin-universal"
elif [[ "$BIN_ARCHS" == *x86_64* ]]; then
  TARBALL_ARCH="darwin-amd64"
elif [[ "$BIN_ARCHS" == *arm64* ]]; then
  TARBALL_ARCH="darwin-arm64"
else
  # lipo unavailable or silent; fall back to the build host arch.
  HOST_ARCH="$(uname -m)"
  [[ "$HOST_ARCH" == "x86_64" ]] && HOST_ARCH="amd64"
  TARBALL_ARCH="darwin-$HOST_ARCH"
fi

OUT_NAME="agentcookie-${VERSION}-${TARBALL_ARCH}"
DIST_DIR="dist"
mkdir -p "$DIST_DIR"
STAGE="$(mktemp -d -t agentcookie-release.XXXXXX)/$OUT_NAME"
mkdir -p "$STAGE"

cp "$BIN" "$STAGE/agentcookie"
cp "$INSTALL_SCRIPT" "$STAGE/install-beta.sh"
cp "$QUICKSTART" "$STAGE/quickstart-beta.md"

chmod +x "$STAGE/agentcookie" "$STAGE/install-beta.sh"

TARBALL_PATH="$DIST_DIR/${OUT_NAME}.tar.gz"
tar -czf "$TARBALL_PATH" -C "$(dirname "$STAGE")" "$OUT_NAME"

SIZE="$(du -h "$TARBALL_PATH" | awk '{print $1}')"
echo "release-tarball.sh: wrote $TARBALL_PATH ($SIZE)"

# Print a SHA-256 so release notes can include an integrity hash.
SHA="$(shasum -a 256 "$TARBALL_PATH" | awk '{print $1}')"
echo "release-tarball.sh: sha256 $SHA"
