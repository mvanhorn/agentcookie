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

# --- wizard_skip_candidate ---
# The installer may only consider skipping the wizard when the requested
# role is already configured and no wizard-forcing argument was passed.
cfg="$(mktemp -d)"
flow="$(mktemp -d)"
trap 'rm -rf "$tmp" "$cfg" "$flow"' EXIT

assert_eq "$(role_config_path source "$cfg")" "$cfg/source.yaml" "source config path"
assert_eq "$(role_config_path sink "$cfg")" "$cfg/sink.yaml" "sink config path"

# Nothing configured yet: a first install must always run the wizard.
if wizard_skip_candidate source "$cfg" 0; then
  fail "unconfigured source must run the wizard"
fi
if wizard_skip_candidate sink "$cfg" 0; then
  fail "unconfigured sink must run the wizard"
fi

printf 'peer:\n  hostname: example\n' > "$cfg/source.yaml"

# Configured role, no forcing args: eligible for the health check.
if ! wizard_skip_candidate source "$cfg" 0; then
  fail "configured source with no forcing args should be a skip candidate"
fi

# The role-mismatch case this guard exists for. doctor exits 0 on a
# healthy source-only box because every sink check is SKIPPED, not FAIL.
# Asking for a sink there must still run the wizard.
if wizard_skip_candidate sink "$cfg" 0; then
  fail "source config must not satisfy an --as sink install"
fi

# Wizard-forcing arguments always run the wizard.
if wizard_skip_candidate source "$cfg" 1; then
  fail "forcing args must force the wizard to run"
fi

# A role the script never accepts is never a skip candidate.
printf 'x\n' > "$cfg/bogus.yaml"
if wizard_skip_candidate bogus "$cfg" 0; then
  fail "unknown role must not be a skip candidate"
fi

# --- role_peer_hostnames / plist_program_path ---
assert_eq "$(role_peer_hostnames "$cfg/source.yaml")" "example" "peer hostname from source.yaml"
cat > "$cfg/sink.yaml" <<'YAML'
listen:
  addr: 0.0.0.0:9999
sinks:
  - url: http://other:9999/sync
    peer: not-this-one
peer:
  hostname: "mac-mini" # quoted, with a comment
skip_chrome_sqlite: true
YAML
assert_eq "$(role_peer_hostnames "$cfg/sink.yaml" | tr '\n' ' ')" "not-this-one mac-mini " "peer hostnames include sinks[].peer and peer.hostname"
cat > "$cfg/multi.yaml" <<'YAML'
sinks:
  - url: http://mini:9999/sync
    peer: mac-mini
  - peer: 'studio' # second sink
    url: http://studio:9999/sync
cdp_source:
  enabled: false
YAML
assert_eq "$(role_peer_hostnames "$cfg/multi.yaml" | tr '\n' ' ')" "mac-mini studio " "peer hostnames from a multi-sink source.yaml"
assert_eq "$(role_peer_hostnames "$cfg/missing.yaml")" "" "missing config has no peer"

# --- installer re-run flow ---
# Each scenario runs the real script end to end against a stub tarball,
# a scratch HOME and bin dir, and stub tailscale, codesign, xattr,
# launchctl and sleep on PATH. Real launchctl is never reached.
stubs="$flow/stubs"
mkdir -p "$stubs" "$flow/pkg/agentcookie_0.0.0_darwin_universal"
for tool in tailscale codesign xattr sleep; do
  printf '#!/usr/bin/env bash\nexit %s\n' "$([[ $tool == codesign ]] && echo 1 || echo 0)" > "$stubs/$tool"
done
cat > "$stubs/launchctl" <<'STUB'
#!/usr/bin/env bash
echo "launchctl $*" >> "$STUB_LOG"
case "$1" in
  print)
    [[ "${STUB_LC_LOADED:-1}" == "1" ]] || exit 113
    printf '%s = {\n\tstate = %s\n' "$2" "${STUB_LC_STATE:-running}"
    [[ -z "${STUB_LC_PROGRAM:-}" ]] || printf '\tprogram = %s\n' "$STUB_LC_PROGRAM"
    printf '}\n'
    ;;
  kickstart) exit "${STUB_LC_KICK_EXIT:-0}" ;;
esac
STUB
cat > "$flow/pkg/agentcookie_0.0.0_darwin_universal/agentcookie" <<'STUB'
#!/usr/bin/env bash
echo "agentcookie $*" >> "$STUB_LOG"
case "$1 ${2:-}" in
  "doctor ")
    echo "stub doctor output"
    exit "${STUB_DOCTOR_EXIT:-0}" ;;
  "wizard install")
    for a in "$@"; do
      if [[ "$a" == "--extra-binary" ]]; then
        echo "unknown flag: --extra-binary" >&2
        exit 64
      fi
    done ;;
esac
exit 0
STUB
chmod +x "$stubs"/* "$flow/pkg/agentcookie_0.0.0_darwin_universal/agentcookie"
tar -czf "$flow/ac.tar.gz" -C "$flow/pkg" .
uid="$(id -u)"

# new_box NAME ROLE... : a scratch Mac where each ROLE is configured with
# peer hostname "peerhost" and a LaunchAgent plist that runs bin/agentcookie.
new_box() {
  local name="$1" r
  shift
  box="$flow/$name"
  mkdir -p "$box/home/.config/agentcookie" "$box/home/Library/LaunchAgents" "$box/bin"
  : > "$box/calls.log"
  for r in "$@"; do
    printf 'peer:\n  hostname: peerhost\n' > "$box/home/.config/agentcookie/$r.yaml"
    write_plist "$r" "$box/bin/agentcookie"
  done
}

write_plist() {
  local r="$1" program="$2"
  cat > "$box/home/Library/LaunchAgents/dev.agentcookie.$r.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.agentcookie.$r</string>

  <key>ProgramArguments</key>
  <array>
    <string>$program</string>
    <string>$r</string>
  </array>
</dict>
</plist>
PLIST
}

# run_installer ARGS... : runs install-beta.sh in $box with no TTY.
# Sets run_exit; output lands in $box/out, calls in $box/calls.log.
run_installer() {
  run_exit=0
  env -u AGENTCOOKIE_INSTALL_BETA_LIB_ONLY \
    HOME="$box/home" PATH="$stubs:$PATH" STUB_LOG="$box/calls.log" \
    bash "$ROOT/scripts/install-beta.sh" --tarball "$flow/ac.tar.gz" --bin-dir "$box/bin" "$@" \
    < /dev/null > "$box/out" 2>&1 || run_exit=$?
}

called() { grep -qF -- "$1" "$box/calls.log"; }
said() { grep -qF -- "$1" "$box/out"; }
wizard_ran() { grep -q '^agentcookie wizard install' "$box/calls.log"; }
# guard_ran_doctor: doctor was called before the wizard (the final doctor
# after the wizard does not count).
guard_ran_doctor() {
  awk '/^agentcookie wizard install/ {exit} /^agentcookie doctor/ {found=1} END {exit !found}' "$box/calls.log"
}

expect_healthy() {
  local label="$1" r="$2"
  assert_eq "$run_exit" "0" "$label: exit code"
  if wizard_ran; then fail "$label: wizard must not run"; fi
  if ! called "launchctl kickstart -k gui/$uid/dev.agentcookie.$r"; then
    fail "$label: expected kickstart -k gui/$uid/dev.agentcookie.$r"
  fi
  if ! said "stub doctor output"; then fail "$label: doctor output not reported"; fi
}

expect_wizard() {
  local label="$1"
  if ! wizard_ran; then fail "$label: wizard must run"; cat "$box/out" >&2; fi
}

# healthy configured source upgrades in place without the wizard.
new_box healthy source
run_installer --as source
expect_healthy "healthy source" source
if ! said "--repair"; then fail "healthy path hint must name --repair"; fi
if called "launchctl kickstart" && ! grep -q 'launchctl print' "$box/calls.log"; then
  fail "healthy source: loaded check must run before kickstart"
fi

# doctor FAIL falls through to the wizard, before any restart.
new_box doctorfail source
STUB_DOCTOR_EXIT=1 run_installer --as source --peer peerhost
expect_wizard "doctor fail"
if called "launchctl kickstart"; then fail "doctor fail: must not restart the daemon"; fi

# LaunchAgent not loaded.
new_box notloaded source
STUB_LC_LOADED=0 run_installer --as source --peer peerhost
expect_wizard "agent not loaded"
if called "launchctl kickstart"; then fail "agent not loaded: must not kickstart"; fi

# plist runs a different binary path (also how a --bin-dir change shows up).
new_box otherpath source
write_plist source "/somewhere/else/agentcookie"
run_installer --as source --peer peerhost
expect_wizard "plist path mismatch"
if called "launchctl kickstart"; then fail "plist path mismatch: must not kickstart"; fi

# launchd still holds a job definition for another binary than the plist.
new_box staleloaded source
STUB_LC_PROGRAM="/old/agentcookie" run_installer --as source --peer peerhost
expect_wizard "loaded job runs another binary"
if called "launchctl kickstart"; then fail "loaded job mismatch: must not kickstart"; fi

# The loaded job runs the target: still healthy.
new_box loadedmatch source
STUB_LC_PROGRAM="$box/bin/agentcookie" run_installer --as source
expect_healthy "loaded job runs the target" source

# kickstart fails.
new_box kickfail source
STUB_LC_KICK_EXIT=1 run_installer --as source --peer peerhost
expect_wizard "kickstart fails"

# kickstart succeeds but the agent is not running afterward.
new_box notrunning source
STUB_LC_STATE=waiting run_installer --as source --peer peerhost
expect_wizard "agent not running after restart"

# Role mismatch: --as sink on a source-only box runs the wizard.
new_box rolemismatch source
run_installer --as sink --peer peerhost --code AAAA-BBBB-CCCC --pair-url http://peerhost:9998/pair
expect_wizard "sink on source-only box"
if guard_ran_doctor; then
  fail "sink on source-only box: guard must not run doctor"
fi

# --no-cdp forces the wizard on a configured sink.
new_box nocdp sink
run_installer --as sink --peer peerhost --code AAAA-BBBB-CCCC --pair-url http://peerhost:9998/pair --no-cdp
expect_wizard "--no-cdp"
if ! called "--no-cdp"; then fail "--no-cdp must be forwarded"; fi

# explicit --skip-keychain-prompt forces the wizard.
new_box explicitskp sink
run_installer --as sink --peer peerhost --code AAAA-BBBB-CCCC --pair-url http://peerhost:9998/pair --skip-keychain-prompt
expect_wizard "explicit --skip-keychain-prompt"

# no TTY on a configured sink (installer adds --skip-keychain-prompt
# itself on the wizard path) still takes the healthy path.
new_box notty sink
run_installer --as sink
expect_healthy "no-TTY sink" sink

# the documented sink command re-run from history takes the healthy path.
new_box documented sink
run_installer --as sink --peer peerhost --code AAAA-BBBB-CCCC --pair-url http://peerhost:9998/pair
expect_healthy "documented sink re-run" sink

# a multi-sink source re-run with one of its configured sink peers is healthy.
new_box multisink source
printf 'sinks:\n  - url: http://peerhost:9999/sync\n    peer: peerhost\n  - url: http://studio:9999/sync\n    peer: studio\n' > "$box/home/.config/agentcookie/source.yaml"
run_installer --as source --peer studio
expect_healthy "multi-sink source re-run" source

# a different --peer runs the wizard.
new_box newpeer source
run_installer --as source --peer otherhost
expect_wizard "different --peer"
if guard_ran_doctor; then
  fail "different --peer: guard must not run doctor"
fi

# --repair runs the wizard and forwards --repair.
new_box repair source
run_installer --as source --peer peerhost --repair
expect_wizard "--repair"
if ! grep -q '^agentcookie wizard install .*--repair' "$box/calls.log"; then
  fail "--repair must be forwarded to wizard install"
fi

# --extra-binary on the healthy path prints guidance only.
new_box extrahealthy sink
run_installer --as sink --extra-binary /path/cli
expect_healthy "--extra-binary healthy" sink
# The guidance must never recommend --recreate: it replaces the Chrome Safe
# Storage key with a random value and makes existing Chrome cookies unreadable.
if said "--recreate"; then fail "--extra-binary healthy: must not recommend --recreate"; fi
if ! said "$box/bin/agentcookie cookies --domain"; then
  fail "--extra-binary healthy: cookies --domain guidance not printed"
fi
if ! said "/path/cli"; then fail "--extra-binary healthy: guidance must name the binary"; fi
if ! said "Developer ID"; then fail "--extra-binary healthy: Developer ID note missing"; fi
if called "set-keychain-access"; then fail "--extra-binary healthy: must not run set-keychain-access"; fi

# --extra-binary on the wizard path is not forwarded to wizard install.
new_box extrawizard sink
run_installer --as sink --peer peerhost --code AAAA-BBBB-CCCC --pair-url http://peerhost:9998/pair --extra-binary /path/cli --repair
expect_wizard "--extra-binary wizard"
assert_eq "$run_exit" "0" "--extra-binary wizard: exit code"
if said "--recreate"; then fail "--extra-binary wizard: must not recommend --recreate"; fi
if ! said "$box/bin/agentcookie cookies --domain"; then
  fail "--extra-binary wizard: cookies --domain guidance not printed"
fi
if called "set-keychain-access"; then fail "--extra-binary wizard: must not run set-keychain-access"; fi

# the binary is renamed into place, leaving no temp file behind.
new_box placement source
printf 'old\n' > "$box/bin/agentcookie"
chmod +x "$box/bin/agentcookie"
# ls -i is the portable inode read (stat flags differ on BSD and GNU).
# shellcheck disable=SC2012
old_inode="$(ls -i "$box/bin/agentcookie" | awk '{print $1}')"
run_installer --as source
# shellcheck disable=SC2012
new_inode="$(ls -i "$box/bin/agentcookie" | awk '{print $1}')"
if [[ "$old_inode" == "$new_inode" ]]; then fail "binary placement must replace the target inode"; fi
assert_eq "$(ls -A "$box/bin")" "agentcookie" "binary placement leaves no temp file"
if ! grep -q 'stub doctor' "$box/bin/agentcookie"; then fail "binary placement did not install the new binary"; fi

# First install: no config, the wizard runs and the guard does not run doctor.
new_box fresh
run_installer --as source --peer peerhost
expect_wizard "first install"
if guard_ran_doctor; then fail "first install: guard must not run doctor"; fi

# Apple Events entitlement (#135). Hardened Runtime blocks chromectl's
# osascript quit unless the signed binary carries the key with a true
# value. Plain grep keeps these checks runnable on the Linux CI runner.
readonly APPLE_EVENTS_KEY="com.apple.security.automation.apple-events"

# entitlements_enable_apple_events FILE: the plist sets the key to <true/>.
entitlements_enable_apple_events() {
  local flat
  flat="$(tr -d '[:space:]' < "$1")"
  [[ "$flat" == *"<key>$APPLE_EVENTS_KEY</key><true/>"* ]]
}

# sign_passes_entitlements FILE: sign.sh hands the entitlements file to
# codesign.
sign_passes_entitlements() {
  # shellcheck disable=SC2016 # the literal $ENTITLEMENTS is the match target
  grep -qE -- '--entitlements "\$ENTITLEMENTS"' "$1"
}

if ! entitlements_enable_apple_events "$ROOT/scripts/agentcookie.entitlements"; then
  fail "agentcookie.entitlements does not set $APPLE_EVENTS_KEY to true"
fi
if command -v plutil >/dev/null 2>&1; then
  if ! plutil -lint "$ROOT/scripts/agentcookie.entitlements" >/dev/null; then
    fail "agentcookie.entitlements is not a valid plist"
  fi
fi
if ! sign_passes_entitlements "$ROOT/scripts/sign.sh"; then
  fail "sign.sh does not pass --entitlements to codesign"
fi

# The static checks must reject a false value and a dropped flag.
sed 's|<true/>|<false/>|' "$ROOT/scripts/agentcookie.entitlements" > "$tmp/false.entitlements"
if entitlements_enable_apple_events "$tmp/false.entitlements"; then
  fail "entitlements check accepted a false Apple Events value"
fi
grep -v -- '--entitlements' "$ROOT/scripts/sign.sh" > "$tmp/sign-no-entitlements.sh"
if sign_passes_entitlements "$tmp/sign-no-entitlements.sh"; then
  fail "sign.sh check accepted a script without --entitlements"
fi

# sign.sh's post-sign check, run against stub security and codesign
# binaries. The stub codesign reports the Apple Events value from
# STUB_APPLE_EVENTS (true or false) and succeeds for every other call.
sign_stubs="$tmp/stubs"
mkdir -p "$sign_stubs"
cat > "$sign_stubs/security" <<'STUB'
#!/usr/bin/env bash
echo '  1) 0000000000000000000000000000000000000000 "stub identity"'
STUB
cat > "$sign_stubs/codesign" <<'STUB'
#!/usr/bin/env bash
for arg in "$@"; do
  if [[ "$arg" == "-d" ]]; then
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>com.apple.security.automation.apple-events</key>\n\t<%s/>\n</dict></plist>\n' "$STUB_APPLE_EVENTS"
    exit 0
  fi
done
exit 0
STUB
chmod +x "$sign_stubs/security" "$sign_stubs/codesign"
printf 'bin' > "$tmp/agentcookie-stub"

# run_sign_with_stub VALUE: prints sign.sh's exit code, stderr to $tmp/sign.err.
run_sign_with_stub() {
  local rc=0
  PATH="$sign_stubs:$PATH" STUB_APPLE_EVENTS="$1" AGENTCOOKIE_SIGN_IDENTITY="stub identity" \
    bash "$ROOT/scripts/sign.sh" "$tmp/agentcookie-stub" >/dev/null 2>"$tmp/sign.err" || rc=$?
  echo "$rc"
}

assert_eq "$(run_sign_with_stub false)" "3" "sign.sh exits 3 when the entitlement is false"
if ! grep -qF "is missing the $APPLE_EVENTS_KEY entitlement" "$tmp/sign.err"; then
  fail "sign.sh did not print the missing-entitlement message for a false value"
fi
assert_eq "$(run_sign_with_stub true)" "0" "sign.sh passes when the entitlement is true"

if [[ $failures -ne 0 ]]; then
  echo "$failures test(s) failed" >&2
  exit 1
fi
echo "release_asset_test.sh: ok"
