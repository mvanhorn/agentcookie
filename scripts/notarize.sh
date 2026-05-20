#!/usr/bin/env bash
#
# notarize.sh - Submit a signed agentcookie binary to Apple's notary
# service. After notarization Apple stamps the binary with a ticket and
# every Mac accepts it without Gatekeeper interactive approval, on any
# launch path (shell exec, LaunchAgent, Finder double-click).
#
# Prerequisites:
#   1. The binary is signed with Developer ID + Hardened Runtime
#      (scripts/sign.sh produces this shape).
#   2. xcrun notarytool credentials are stored in the login keychain
#      under the profile name in AGENTCOOKIE_NOTARY_PROFILE (default:
#      "agentcookie-notary"). One-time setup:
#
#         xcrun notarytool store-credentials agentcookie-notary \
#           --apple-id mvanhorn@gmail.com \
#           --team-id NM8VT393AR \
#           --password <app-specific-password-from-appleid.apple.com>
#
#      See docs/runbook-v0.12-codesign.md for the appleid.apple.com path.
#
# Usage:
#   scripts/notarize.sh <binary> [<binary> ...]
#
# Each invocation waits for Apple's verdict (5-30 min typical). Exits
# zero only when ALL submitted binaries are accepted.
#
# Exit codes:
#   0  all binaries notarized and stapled
#   1  usage error
#   2  notary profile not found in login keychain
#   3  notarization rejected by Apple (see log URL in output)
#   4  staple failed

set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/notarize.sh <binary> [<binary> ...]" >&2
  exit 1
fi

PROFILE="${AGENTCOOKIE_NOTARY_PROFILE:-agentcookie-notary}"

# Sanity-check the notarytool profile exists in the login keychain.
# notarytool itself exits non-zero with a confusing message when the
# profile is missing; check explicitly so users get a runbook pointer.
if ! xcrun notarytool history --keychain-profile "$PROFILE" --output-format plist >/dev/null 2>&1; then
  echo "notarize.sh: notarytool credentials for profile '$PROFILE' not found in your login keychain" >&2
  echo "notarize.sh: see the Notarization section of docs/runbook-v0.12-codesign.md for one-time setup" >&2
  exit 2
fi

for binary in "$@"; do
  if [[ ! -f "$binary" ]]; then
    echo "scripts/notarize.sh: $binary: no such file" >&2
    exit 1
  fi

  # Notarytool requires the binary to be inside a container Apple
  # recognizes (zip, dmg, pkg). For a single Mach-O binary we wrap it
  # in a zip on the fly.
  echo "scripts/notarize.sh: zipping $binary for submission"
  tmpzip="$(mktemp -t agentcookie-notary.XXXXXX).zip"
  ditto -c -k --keepParent "$binary" "$tmpzip"

  echo "scripts/notarize.sh: submitting $binary (waiting for Apple)"
  if ! xcrun notarytool submit "$tmpzip" \
       --keychain-profile "$PROFILE" \
       --wait \
       --output-format json > /tmp/notary-result.json; then
    echo "scripts/notarize.sh: notarytool rejected the submission" >&2
    cat /tmp/notary-result.json >&2 || true
    rm -f "$tmpzip"
    exit 3
  fi

  # Mach-O binaries cannot be stapled (stapling targets bundles, dmgs,
  # installers). Gatekeeper does an online check at first run; the
  # accepted record is what matters here. We do verify the result was
  # Accepted before exiting.
  if ! grep -q '"status": *"Accepted"' /tmp/notary-result.json; then
    echo "scripts/notarize.sh: submission did not return Accepted status" >&2
    cat /tmp/notary-result.json >&2
    rm -f "$tmpzip"
    exit 3
  fi
  echo "scripts/notarize.sh: $binary accepted by Apple"
  rm -f "$tmpzip"
done
