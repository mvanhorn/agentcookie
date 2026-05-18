#!/usr/bin/env bash
#
# sign.sh - Sign a macOS binary with the agentcookie Developer ID identity.
#
# Reads AGENTCOOKIE_SIGN_IDENTITY from the environment, falling back to the
# maintainer's Developer ID Application identity. Hardened Runtime
# (--options runtime) and secure timestamping (--timestamp) are required so
# the result qualifies for notarization later. --force is required so a
# previously-signed binary can be re-signed in place (steady-state behavior
# after every `go install`).
#
# Usage:
#   scripts/sign.sh <binary> [<binary> ...]
#
# Environment:
#   AGENTCOOKIE_SIGN_IDENTITY  codesign identity string (CN of the cert,
#                              or the SHA-1 fingerprint).
#                              Default: "Developer ID Application: Matthew
#                              Charles Van Horn (NM8VT393AR)"
#
# Exit codes:
#   0  success
#   1  usage error
#   2  identity not available on this build machine
#   3  codesign or verify failed
#
# See docs/runbook-v0.12-codesign.md for cert install / renewal steps.

set -euo pipefail

readonly DEFAULT_IDENTITY="Developer ID Application: Matthew Charles Van Horn (NM8VT393AR)"
readonly RUNBOOK="docs/runbook-v0.12-codesign.md"

IDENTITY="${AGENTCOOKIE_SIGN_IDENTITY:-$DEFAULT_IDENTITY}"

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/sign.sh <binary> [<binary> ...]" >&2
  exit 1
fi

# Confirm the identity is in the codesigning keychain before we burn time on
# codesign(1)'s own opaque error. `security find-identity -v -p codesigning`
# lists every cert that can sign on this machine; grep matches either the CN
# or the SHA-1 fingerprint form.
if ! security find-identity -v -p codesigning 2>/dev/null | grep -qF "$IDENTITY"; then
  cat >&2 <<EOF
scripts/sign.sh: codesign identity not found on this machine.

  identity: $IDENTITY

Run \`security find-identity -v -p codesigning\` to see what is available.
To install the Developer ID Application cert on a fresh build machine, see
$RUNBOOK.

To override the identity (e.g., a contributor's own cert), set
AGENTCOOKIE_SIGN_IDENTITY before invoking make / scripts/sign.sh.
EOF
  exit 2
fi

for binary in "$@"; do
  if [[ ! -f "$binary" ]]; then
    echo "scripts/sign.sh: $binary: no such file" >&2
    exit 1
  fi

  echo "scripts/sign.sh: signing $binary"
  codesign \
    --force \
    --options runtime \
    --timestamp \
    --sign "$IDENTITY" \
    "$binary"

  echo "scripts/sign.sh: verifying $binary"
  codesign --verify --deep --strict --verbose=2 "$binary"
done

echo "scripts/sign.sh: done"
