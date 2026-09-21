#!/usr/bin/env bash
#
# release_asset_test.sh - Selection tests for the macOS installer and the
# release-tarball archive name. No extra test framework; run:
#
#   bash scripts/release_asset_test.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AGENTCOOKIE_INSTALL_BETA_LIB_ONLY=1
# shellcheck disable=SC1091
source "$ROOT/scripts/install-beta.sh"
AGENTCOOKIE_RELEASE_TARBALL_LIB_ONLY=1
# shellcheck disable=SC1091
source "$ROOT/scripts/release-tarball.sh"

failures=0

fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

assert_eq() {
  local got="$1" want="$2" label="$3"
  if [[ "$got" != "$want" ]]; then
    fail "$label: expected [$want] got [$got]"
  fi
}

# rows_for HOST JSON
rows_for() {
  local host="$1"
  local json="$2"
  printf '%s\n' "$json" | jq -r -f <(macos_release_jq "$host")
}

# select_for HOST JSON
# Prints tag, asset, used_prerelease, latest_stable on one line separated
# by | so tests can split without fighting empty fields.
select_for() {
  local host="$1"
  local json="$2"
  local out tag asset pre latest
  out="$(select_macos_release_from_rows "$(rows_for "$host" "$json")")"
  IFS=$'\037' read -r tag asset pre latest <<< "$out"
  printf '%s|%s|%s|%s\n' "$tag" "$asset" "$pre" "$latest"
}

stable() {
  local tag="$1" published="$2" assets_json="$3"
  cat <<EOF
{"tag_name":"$tag","draft":false,"prerelease":false,"published_at":"$published","assets":$assets_json}
EOF
}

pre() {
  local tag="$1" published="$2" assets_json="$3"
  cat <<EOF
{"tag_name":"$tag","draft":false,"prerelease":true,"published_at":"$published","assets":$assets_json}
EOF
}

draft() {
  local tag="$1" published="$2" assets_json="$3"
  cat <<EOF
{"tag_name":"$tag","draft":true,"prerelease":false,"published_at":"$published","assets":$assets_json}
EOF
}

assets() {
  local names=("$@")
  local i name out="["
  for i in "${!names[@]}"; do
    name="${names[$i]}"
    [[ $i -gt 0 ]] && out+=","
    out+="{\"name\":\"$name\"}"
  done
  out+="]"
  printf '%s\n' "$out"
}

array_of() {
  local first=1 item
  printf '['
  for item in "$@"; do
    [[ $first -eq 1 ]] && first=0 || printf ','
    printf '%s' "$item"
  done
  printf ']\n'
}

linux_only="$(assets "agentcookie_1.1.0_linux_amd64.tar.gz" "agentcookie_1.1.0_linux_arm64.tar.gz")"
both_arch="$(assets \
  "agentcookie_1.2.0_darwin_amd64.tar.gz" \
  "agentcookie_1.2.0_darwin_arm64.tar.gz")"
universal_and_arch="$(assets \
  "agentcookie_1.3.0_darwin_amd64.tar.gz" \
  "agentcookie_1.3.0_darwin_arm64.tar.gz" \
  "agentcookie_1.3.0_darwin_universal.tar.gz")"
hyphen_universal="$(assets "agentcookie-v0.9.0-darwin-universal.tar.gz")"
legacy_arm="$(assets "agentcookie-v0.17.1-darwin-arm64.tar.gz")"
legacy_amd="$(assets "agentcookie-v0.17.1-darwin-amd64.tar.gz")"
x86_name="$(assets "agentcookie_1.0.0_darwin_x86_64.tar.gz")"
arm_only="$(assets "agentcookie_1.0.0_darwin_arm64.tar.gz")"
amd_only="$(assets "agentcookie_1.0.0_darwin_amd64.tar.gz")"

# Newest stable is linux-only; the previous stable has arm64. Apple
# Silicon must walk back, and must not treat linux_arm64 as darwin.
json="$(array_of \
  "$(stable v1.1.0 2026-09-01T00:00:00Z "$linux_only")" \
  "$(stable v1.0.0 2026-08-01T00:00:00Z "$arm_only")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.0.0|agentcookie_1.0.0_darwin_arm64.tar.gz|0|v1.1.0" \
  "walk past linux-only latest"
assert_eq "$(select_for amd64 "$json")" \
  "||0|v1.1.0" \
  "intel does not take an arm64-only asset"

# Both darwin archs exist. Lexical order would pick darwin_amd64 first
# (amd64 < arm64). Selection follows the host.
json="$(array_of "$(stable v1.2.0 2026-09-02T00:00:00Z "$both_arch")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.2.0|agentcookie_1.2.0_darwin_arm64.tar.gz|0|v1.2.0" \
  "arm64 host does not take darwin_amd64"
assert_eq "$(select_for amd64 "$json")" \
  "v1.2.0|agentcookie_1.2.0_darwin_amd64.tar.gz|0|v1.2.0" \
  "amd64 host does not take darwin_arm64"

# Universal wins over either arch-specific asset, on both hosts.
json="$(array_of "$(stable v1.3.0 2026-09-03T00:00:00Z "$universal_and_arch")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.3.0|agentcookie_1.3.0_darwin_universal.tar.gz|0|v1.3.0" \
  "arm64 prefers darwin_universal"
assert_eq "$(select_for amd64 "$json")" \
  "v1.3.0|agentcookie_1.3.0_darwin_universal.tar.gz|0|v1.3.0" \
  "amd64 prefers darwin_universal"

# Hyphenated universal, and legacy hyphen arch names.
json="$(array_of "$(stable v0.9.0 2026-01-01T00:00:00Z "$hyphen_universal")")"
assert_eq "$(select_for arm64 "$json")" \
  "v0.9.0|agentcookie-v0.9.0-darwin-universal.tar.gz|0|v0.9.0" \
  "hyphen universal"
json="$(array_of "$(stable v0.17.1 2026-02-01T00:00:00Z "$legacy_arm")")"
assert_eq "$(select_for arm64 "$json")" \
  "v0.17.1|agentcookie-v0.17.1-darwin-arm64.tar.gz|0|v0.17.1" \
  "legacy hyphen arm64"
assert_eq "$(select_for amd64 "$json")" \
  "||0|v0.17.1" \
  "legacy arm64 is not an intel asset"
json="$(array_of "$(stable v0.17.1 2026-02-01T00:00:00Z "$legacy_amd")")"
assert_eq "$(select_for amd64 "$json")" \
  "v0.17.1|agentcookie-v0.17.1-darwin-amd64.tar.gz|0|v0.17.1" \
  "legacy hyphen amd64"
json="$(array_of "$(stable v1.0.0 2026-03-01T00:00:00Z "$x86_name")")"
assert_eq "$(select_for amd64 "$json")" \
  "v1.0.0|agentcookie_1.0.0_darwin_x86_64.tar.gz|0|v1.0.0" \
  "darwin_x86_64 spelling"
assert_eq "$(select_for arm64 "$json")" \
  "||0|v1.0.0" \
  "darwin_x86_64 is not arm64"

# Drafts are ignored. A newer prerelease does not beat an older stable.
json="$(array_of \
  "$(draft v9.9.9 2026-12-01T00:00:00Z "$universal_and_arch")" \
  "$(pre v1.4.0-rc.1 2026-10-01T00:00:00Z "$universal_and_arch")" \
  "$(stable v1.0.0 2026-08-01T00:00:00Z "$arm_only")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.0.0|agentcookie_1.0.0_darwin_arm64.tar.gz|0|v1.0.0" \
  "stable beats newer prerelease; draft ignored"

# A prerelease-only history has no latest stable tag. The empty field
# must survive the unit-separator split.
json="$(array_of "$(pre v1.2.0-rc.1 2026-08-15T00:00:00Z "$universal_and_arch")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.2.0-rc.1|agentcookie_1.3.0_darwin_universal.tar.gz|1|" \
  "prerelease only, empty latest stable"

# Prerelease is used only when no stable release has a usable asset.
json="$(array_of \
  "$(stable v1.1.0 2026-09-01T00:00:00Z "$linux_only")" \
  "$(pre v1.2.0-rc.1 2026-08-15T00:00:00Z "$amd_only")")"
assert_eq "$(select_for amd64 "$json")" \
  "v1.2.0-rc.1|agentcookie_1.0.0_darwin_amd64.tar.gz|1|v1.1.0" \
  "prerelease fallback"
assert_eq "$(select_for arm64 "$json")" \
  "||0|v1.1.0" \
  "prerelease amd64 is not used on arm64"

# Newest release has only the wrong arch; an older one has the right arch.
json="$(array_of \
  "$(stable v1.4.0 2026-09-04T00:00:00Z "$amd_only")" \
  "$(stable v1.0.0 2026-08-01T00:00:00Z "$arm_only")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.0.0|agentcookie_1.0.0_darwin_arm64.tar.gz|0|v1.4.0" \
  "skip newer wrong-arch stable"
assert_eq "$(select_for amd64 "$json")" \
  "v1.4.0|agentcookie_1.0.0_darwin_amd64.tar.gz|0|v1.4.0" \
  "intel takes the newest amd64 asset"

# gh api --paginate --jq runs the filter per page and concatenates.
# Page 1 is newer. The bash selector must keep that order.
page1="$(array_of "$(stable v1.1.0 2026-09-01T00:00:00Z "$linux_only")")"
page2="$(array_of "$(stable v1.0.0 2026-08-01T00:00:00Z "$arm_only")")"
paged="$(printf '%s\n%s\n' "$(rows_for arm64 "$page1")" "$(rows_for arm64 "$page2")")"
out="$(select_macos_release_from_rows "$paged")"
IFS=$'\037' read -r tag asset pre latest <<< "$out"
assert_eq "$tag|$asset|$pre|$latest" \
  "v1.0.0|agentcookie_1.0.0_darwin_arm64.tar.gz|0|v1.1.0" \
  "concatenated pages stay newest-first"

# Published order inside one payload is not assumed to be pre-sorted.
json="$(array_of \
  "$(stable v1.0.0 2026-08-01T00:00:00Z "$arm_only")" \
  "$(stable v1.1.0 2026-09-01T00:00:00Z "$linux_only")")"
assert_eq "$(select_for arm64 "$json")" \
  "v1.0.0|agentcookie_1.0.0_darwin_arm64.tar.gz|0|v1.1.0" \
  "sort by published_at inside the page"

# Empty glob and exact basename, not lexical head.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
if pick_downloaded_tarball "$tmp" "agentcookie_1.0.0_darwin_arm64.tar.gz" >/dev/null 2>&1; then
  fail "empty download dir should not yield a tarball"
fi
printf 'x' > "$tmp/agentcookie_1.0.0_darwin_amd64.tar.gz"
printf 'x' > "$tmp/agentcookie_1.0.0_darwin_arm64.tar.gz"
got="$(pick_downloaded_tarball "$tmp" "agentcookie_1.0.0_darwin_arm64.tar.gz")"
assert_eq "$(basename "$got")" "agentcookie_1.0.0_darwin_arm64.tar.gz" \
  "pick expected asset, not lexical first"
if pick_downloaded_tarball "$tmp" "agentcookie_1.0.0_darwin_universal.tar.gz" >/dev/null 2>&1; then
  fail "missing expected asset should fail even when other tarballs exist"
fi

asset_usable_for_host "agentcookie_1.0.0_darwin_universal.tar.gz" arm64
asset_usable_for_host "agentcookie-v0.9.0-darwin-universal.tar.gz" amd64
asset_usable_for_host "agentcookie_1.0.0_darwin_arm64.tar.gz" arm64
asset_usable_for_host "agentcookie-v0.17.1-darwin-amd64.tar.gz" amd64
asset_usable_for_host "agentcookie_1.0.0_darwin_x86_64.tar.gz" amd64
if asset_usable_for_host "agentcookie_1.1.0_linux_arm64.tar.gz" arm64; then
  fail "linux_arm64 must not count as darwin"
fi
if asset_usable_for_host "agentcookie_1.0.0_darwin_amd64.tar.gz" arm64; then
  fail "darwin_amd64 must not count as arm64"
fi
if asset_usable_for_host "agentcookie_1.0.0_darwin_arm64.tar.gz" amd64; then
  fail "darwin_arm64 must not count as amd64"
fi
if asset_usable_for_host "notes.txt" arm64; then
  fail "non-tarball must not count"
fi

assert_eq "$(macos_host_arch arm64)" "arm64" "host arm64"
assert_eq "$(macos_host_arch aarch64)" "arm64" "host aarch64"
assert_eq "$(macos_host_arch x86_64)" "amd64" "host x86_64"
assert_eq "$(macos_host_arch amd64)" "amd64" "host amd64"
if macos_host_arch ppc64le >/dev/null 2>&1; then
  fail "unknown machine should fail"
fi

assert_eq "$(tarball_arch_from_lipo_archs "arm64 x86_64")" "darwin_universal" "lipo arm64 x86_64"
assert_eq "$(tarball_arch_from_lipo_archs "x86_64 arm64")" "darwin_universal" "lipo x86_64 arm64"
assert_eq "$(tarball_arch_from_lipo_archs "arm64")" "darwin_arm64" "lipo arm64"
assert_eq "$(tarball_arch_from_lipo_archs "x86_64")" "darwin_amd64" "lipo x86_64"
if tarball_arch_from_lipo_archs "" >/dev/null 2>&1; then
  fail "empty lipo archs should fail"
fi
if tarball_arch_from_lipo_archs "ppc" >/dev/null 2>&1; then
  fail "unknown lipo arch should fail"
fi

# The installer and the workflow must not hide gh failures as "no asset".
if grep -nE 'gh (api|release).*\|\| true' "$ROOT/scripts/install-beta.sh" >/dev/null; then
  fail "install-beta.sh still swallows a gh failure with || true"
fi
if grep -n 'ls .*-1 .*head' "$ROOT/scripts/install-beta.sh" >/dev/null; then
  fail "install-beta.sh still picks a tarball with ls | head"
fi
if ! grep -q 'if-no-files-found: error' "$ROOT/.github/workflows/release.yml"; then
  fail "release.yml upload does not fail when the universal tarball is missing"
fi
if ! grep -q 'dist/\*darwin\*universal\*.tar.gz' "$ROOT/.github/workflows/release.yml"; then
  fail "release.yml does not require a universal darwin archive"
fi
# A v* tag is attacker-controlled. ${{ github.ref_name }} must be assigned
# to RELEASE_TAG and only then expanded as "$RELEASE_TAG" inside run scripts.
ref_lines="$(grep -n 'github\.ref_name' "$ROOT/.github/workflows/release.yml" || true)"
if [[ -z "$ref_lines" ]]; then
  fail "release.yml no longer passes the tag through RELEASE_TAG"
fi
while IFS= read -r line; do
  if [[ "$line" != *'RELEASE_TAG:'* ]]; then
    fail "github.ref_name is expanded outside a RELEASE_TAG env assignment: $line"
  fi
done <<< "$ref_lines"
if ! grep -q 'bash scripts/release_asset_test.sh' "$ROOT/.github/workflows/ci.yml"; then
  fail "ci.yml does not run scripts/release_asset_test.sh"
fi
if ! grep -q 'bash scripts/release_asset_test.sh' "$ROOT/Makefile"; then
  fail "make test does not run scripts/release_asset_test.sh"
fi

if [[ $failures -ne 0 ]]; then
  echo "$failures test(s) failed" >&2
  exit 1
fi
echo "release_asset_test.sh: ok"
