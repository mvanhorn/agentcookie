#!/usr/bin/env bash
#
# release-tarball.sh - Build the release tarball for the darwin binary.
# Bundles the notarized agentcookie binary with the install-beta.sh script
# and the quickstart guide.
#
# Usage:
#   scripts/release-tarball.sh <version>
#
# Where <version> matches the release tag (e.g. v1.0.0). The archive name
# comes from `lipo -archs` on bin/agentcookie, using the v1.0 underscore
# scheme (install-beta.sh also accepts the older hyphenated spelling):
#
#   dist/agentcookie_<version-without-v>_darwin_universal.tar.gz
#   dist/agentcookie_<version-without-v>_darwin_arm64.tar.gz
#   dist/agentcookie_<version-without-v>_darwin_amd64.tar.gz
#
# A `make release` / `make build-universal` binary carries both arm64 and
# x86_64 and ships as darwin_universal. A single-arch `make build` dev
# binary ships as darwin_arm64 or darwin_amd64.
#
# Example: v1.0.0 + universal -> dist/agentcookie_1.0.0_darwin_universal.tar.gz
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

# tarball_arch_from_lipo_archs ARCHS
# Map `lipo -archs` output to the archive token. Prints one of
# darwin_universal, darwin_arm64, darwin_amd64. Returns 1 when the
# binary's architectures are missing or unrecognized so the caller does
# not guess a name (a guessed name is how the universal upload glob
# silently matches nothing).
tarball_arch_from_lipo_archs() {
  local archs="$1"
  local has_arm64=0 has_x86_64=0 a
  # lipo prints space-separated arch names; splitting them is the point.
  # shellcheck disable=SC2086
  for a in $archs; do
    case "$a" in
      arm64) has_arm64=1 ;;
      x86_64) has_x86_64=1 ;;
    esac
  done
  if [[ $has_arm64 -eq 1 && $has_x86_64 -eq 1 ]]; then
    printf 'darwin_universal\n'
  elif [[ $has_x86_64 -eq 1 ]]; then
    printf 'darwin_amd64\n'
  elif [[ $has_arm64 -eq 1 ]]; then
    printf 'darwin_arm64\n'
  else
    echo "release-tarball.sh: unrecognized architectures from lipo: ${archs:-<empty>}" >&2
    return 1
  fi
}

if [[ "${AGENTCOOKIE_RELEASE_TARBALL_LIB_ONLY:-}" == "1" ]]; then
  if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
  fi
  echo "release-tarball.sh: AGENTCOOKIE_RELEASE_TARBALL_LIB_ONLY is set; not building a tarball" >&2
  exit 0
fi

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/release-tarball.sh <version>" >&2
  exit 1
fi
VERSION="$1"

# Strip leading 'v' from version for archive naming
VERSION_NUM="${VERSION#v}"

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

# Name the archive for the binary's actual slices. Do not fall back to
# `uname -m`: a wrong or empty lipo result must fail the release instead
# of publishing a single-arch file the universal upload glob will skip.
if ! BIN_ARCHS="$(lipo -archs "$BIN")"; then
  echo "release-tarball.sh: lipo -archs failed on $BIN; refusing to guess an archive name" >&2
  exit 2
fi
if ! TARBALL_ARCH="$(tarball_arch_from_lipo_archs "$BIN_ARCHS")"; then
  exit 2
fi

# Underscore naming, same scheme as the linux archives:
# agentcookie_1.0.0_darwin_universal
OUT_NAME="agentcookie_${VERSION_NUM}_${TARBALL_ARCH}"
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
