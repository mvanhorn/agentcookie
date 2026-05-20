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
#   dist/agentcookie-<version>-darwin-arm64.tar.gz
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

ARCH="$(uname -m)"
if [[ "$ARCH" == "arm64" ]]; then
  TARBALL_ARCH="darwin-arm64"
else
  TARBALL_ARCH="darwin-$ARCH"
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
