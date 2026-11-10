#!/usr/bin/env bash
#
# linux-release-pins.sh - Hash the release's Linux tarballs for the darwin
# build to embed.
#
# The laptop's pairing notice pins one SHA256 per Linux tarball, and the
# darwin binary carries those digests from its build (it never downloads a
# release to compute them). So the Linux tarballs must be built first, then
# hashed here, then the darwin binary built with the digests as ldflags.
#
# Usage:
#   scripts/linux-release-pins.sh <tag> [dist-dir]
#       Print the make arguments for the darwin build, on one line:
#         LINUX_SHA256_AMD64=<sha> LINUX_SHA256_ARM64=<sha>
#       e.g. make release $(scripts/linux-release-pins.sh v1.3.0)
#       (`make release-pinned TAG=<tag>` does exactly this.)
#
#   scripts/linux-release-pins.sh --check-binary <binary> <tag> [dist-dir]
#       Verify that <binary> embeds every digest dist-dir would pin now.
#       Run it right before publishing: a Linux tarball rebuilt after the
#       darwin build fails here instead of shipping a pin that no longer
#       matches the published asset.
#
# dist-dir defaults to dist/ (where goreleaser writes
# agentcookie_<version>_linux_<arch>.tar.gz and checksums.txt). The
# linux_amd64 tarball is required; a missing linux_arm64 tarball pins amd64
# only (arm64 teammates are then refused by teammate-install.sh). When
# dist-dir has a checksums.txt that lists a tarball, its digest must agree.
#
# Exit codes: 0 ok, 2 usage / bad tag / missing amd64 tarball, 3 digest
# disagreement (checksums.txt or --check-binary).

set -euo pipefail

usage() {
  echo "usage: scripts/linux-release-pins.sh [--check-binary <binary>] <tag> [dist-dir]" >&2
  exit 2
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

check_binary=""
if [[ "${1:-}" == "--check-binary" ]]; then
  [[ $# -ge 3 ]] || usage
  check_binary="$2"
  shift 2
fi
[[ $# -ge 1 && $# -le 2 ]] || usage
tag="$1"
dist="${2:-dist}"

# Same rule as the binary (internal/cli releaseTagRE): a published tag,
# never `git describe` output between tags, -dirty, or a snapshot.
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc|ci)(\.?[0-9]+)?)?$ ]]; then
  echo "linux-release-pins.sh: '$tag' is not a release tag (need e.g. v1.3.0 or v1.3.0-rc.1)" >&2
  exit 2
fi
ver="${tag#v}"

# digest_for ARCH: prints the tarball's SHA256, or nothing when absent.
digest_for() {
  local arch="$1" name file sum listed
  name="agentcookie_${ver}_linux_${arch}.tar.gz"
  file="$dist/$name"
  [[ -f "$file" ]] || return 0
  sum="$(sha256_of "$file")"
  if [[ -f "$dist/checksums.txt" ]]; then
    listed="$(awk -v n="$name" '$2 == n || $2 == "*" n {print $1}' "$dist/checksums.txt")"
    if [[ -n "$listed" && "$listed" != "$sum" ]]; then
      echo "linux-release-pins.sh: $name hashes to $sum but $dist/checksums.txt says $listed; rebuild dist/ from scratch" >&2
      return 3
    fi
  fi
  printf '%s\n' "$sum"
}

amd64="$(digest_for amd64)"
if [[ -z "$amd64" ]]; then
  echo "linux-release-pins.sh: missing $dist/agentcookie_${ver}_linux_amd64.tar.gz; build the Linux tarballs for $tag first" >&2
  exit 2
fi
arm64="$(digest_for arm64)"
if [[ -z "$arm64" ]]; then
  echo "linux-release-pins.sh: warning: no $dist/agentcookie_${ver}_linux_arm64.tar.gz; pinning linux_amd64 only (arm64 teammates cannot install $tag)" >&2
fi

if [[ -n "$check_binary" ]]; then
  if [[ ! -f "$check_binary" ]]; then
    echo "linux-release-pins.sh: no binary at $check_binary" >&2
    exit 2
  fi
  for d in "$amd64" "$arm64"; do
    [[ -n "$d" ]] || continue
    if ! LC_ALL=C grep -aqF "$d" "$check_binary"; then
      echo "linux-release-pins.sh: $check_binary does not embed $d from $dist; rebuild it with make release-pinned TAG=$tag" >&2
      exit 3
    fi
  done
  echo "linux-release-pins.sh: $check_binary embeds the $tag Linux digests from $dist" >&2
  exit 0
fi

out="LINUX_SHA256_AMD64=$amd64"
if [[ -n "$arm64" ]]; then
  out+=" LINUX_SHA256_ARM64=$arm64"
fi
printf '%s\n' "$out"
