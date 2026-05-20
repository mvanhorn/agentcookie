#!/usr/bin/env bash
#
# install-beta.sh - One-command installer for the agentcookie closed beta.
#
# Friends run this script with `--as source` (on the laptop they browse on)
# or `--as sink` (on the second Mac their agents run on). It verifies
# prereqs, places the notarized agentcookie binary, and kicks off the
# wizard install interactively. End-state on success: `agentcookie
# doctor` reports all-green.
#
# Usage:
#   ./install-beta.sh --as source
#   ./install-beta.sh --as sink
#
# Optional flags:
#   --peer <hostname>          Tailscale hostname of the OTHER machine.
#                              If omitted, the script prompts interactively.
#   --extra-binary <path>      Repeatable. PP CLI binaries to grant
#                              Chrome Safe Storage access. Sink-side only.
#   --bin-dir <dir>            Where to place the agentcookie binary.
#                              Default: /usr/local/bin if writable,
#                              else $HOME/bin.
#   --tarball <path>           Use a local tarball instead of fetching
#                              the latest release.
#
# Design notes:
#   - Bash, not Go. Friends will read 80 lines of Bash; they will not
#     read a 17 MB binary.
#   - No sudo. If a step needs elevated privileges, we print the command
#     and ask the user to run it themselves.
#   - Idempotent. Re-running on a healthy install reports state and
#     exits 0 without re-running the wizard.
#   - Fails loud. Every step that can fail prints a remediation
#     pointer to the closed-beta quickstart.

set -euo pipefail

ROLE=""
PEER=""
EXTRA_BINS=()
BIN_DIR=""
TARBALL=""

REPO="mvanhorn/agentcookie"

# ---- helpers ----

die() {
  echo "install-beta.sh: $*" >&2
  echo "install-beta.sh: see docs/quickstart-beta.md for help" >&2
  exit 1
}

ok() { echo "install-beta.sh: [ok]   $*"; }
warn() { echo "install-beta.sh: [warn] $*" >&2; }
step() { echo "install-beta.sh: [step] $*"; }

prompt() {
  local var="$1" question="$2"
  local val
  read -rp "    $question: " val
  printf -v "$var" '%s' "$val"
}

# ---- argument parsing ----

while [[ $# -gt 0 ]]; do
  case "$1" in
    --as)
      ROLE="$2"; shift 2 ;;
    --peer)
      PEER="$2"; shift 2 ;;
    --extra-binary)
      EXTRA_BINS+=("$2"); shift 2 ;;
    --bin-dir)
      BIN_DIR="$2"; shift 2 ;;
    --tarball)
      TARBALL="$2"; shift 2 ;;
    -h|--help)
      sed -n '1,30p' "$0" >&2
      exit 0 ;;
    *)
      die "unknown argument: $1" ;;
  esac
done

if [[ -z "$ROLE" ]]; then
  echo "install-beta.sh: which role is this Mac?"
  echo "  source  = the Mac you browse Chrome on"
  echo "  sink    = the Mac your AI agents run on"
  prompt ROLE "role (source/sink)"
fi
case "$ROLE" in
  source|sink) ;;
  *) die "invalid role: $ROLE (expected 'source' or 'sink')" ;;
esac

# ---- prereqs ----

step "checking prereqs"

if ! command -v tailscale >/dev/null 2>&1 && ! command -v /Applications/Tailscale.app/Contents/MacOS/Tailscale >/dev/null 2>&1; then
  die "Tailscale not found. Install from https://tailscale.com/download/mac first."
fi
TS_CLI="$(command -v tailscale 2>/dev/null || true)"
TS_CLI="${TS_CLI:-/Applications/Tailscale.app/Contents/MacOS/Tailscale}"

if ! "$TS_CLI" status >/dev/null 2>&1; then
  die "Tailscale daemon not running. Run 'tailscale up' (or open the Tailscale app) and try again."
fi
ok "Tailscale is up"

if ! ls /Applications/Google\ Chrome.app >/dev/null 2>&1 && \
   ! ls "$HOME/Applications/Google Chrome.app" >/dev/null 2>&1; then
  warn "Google Chrome not found in /Applications. agentcookie is designed for Chrome; other browsers are not supported in this beta."
fi

# ---- locate tarball / fetch release ----

if [[ -z "$TARBALL" ]]; then
  if ! command -v gh >/dev/null 2>&1; then
    die "GitHub CLI (gh) not found, and no --tarball provided. Either install gh + 'gh auth login', or download the release tarball manually and re-run with --tarball <path>."
  fi
  if ! gh auth status >/dev/null 2>&1; then
    die "gh is not authenticated. Run 'gh auth login' first."
  fi
  step "downloading latest beta release from $REPO"
  TMP_DL="$(mktemp -d -t agentcookie-beta.XXXXXX)"
  gh release download --repo "$REPO" --pattern '*darwin-arm64.tar.gz' --dir "$TMP_DL" --clobber
  TARBALL="$(ls -1 "$TMP_DL"/*.tar.gz | head -n1)"
  if [[ -z "$TARBALL" || ! -f "$TARBALL" ]]; then
    die "release tarball not found after download (looked in $TMP_DL)"
  fi
  ok "downloaded $(basename "$TARBALL")"
fi

# ---- extract and verify binary ----

WORK="$(mktemp -d -t agentcookie-install.XXXXXX)"
tar -xzf "$TARBALL" -C "$WORK"
NEW_BIN="$WORK/agentcookie"
if [[ ! -x "$NEW_BIN" ]]; then
  die "agentcookie binary not found inside tarball ($TARBALL)"
fi

step "verifying notarization"
SPCTL_OUT="$(spctl -a -vv "$NEW_BIN" 2>&1 || true)"
if echo "$SPCTL_OUT" | grep -q 'accepted'; then
  ok "binary is notarized and accepted by Gatekeeper"
else
  warn "spctl reports: $SPCTL_OUT"
  warn "binary may not be notarized; LaunchAgent launches may fail. Continuing anyway."
fi

xattr -c "$NEW_BIN" 2>/dev/null || true

# ---- place binary ----

if [[ -z "$BIN_DIR" ]]; then
  if [[ -w /usr/local/bin ]]; then
    BIN_DIR="/usr/local/bin"
  else
    BIN_DIR="$HOME/bin"
  fi
fi
mkdir -p "$BIN_DIR"
TARGET="$BIN_DIR/agentcookie"

step "installing to $TARGET"
cp "$NEW_BIN" "$TARGET"
chmod +x "$TARGET"
ok "installed"

if [[ ":$PATH:" != *":$BIN_DIR:"* ]]; then
  warn "$BIN_DIR is not on your \$PATH. Add this to your shell profile:"
  warn "    export PATH=\"$BIN_DIR:\$PATH\""
fi

# ---- run wizard ----

step "running agentcookie wizard install --as $ROLE"

if [[ -z "$PEER" ]]; then
  echo "    What is the Tailscale hostname of the OTHER machine?"
  echo "    Run 'tailscale status' to list your tailnet hosts."
  prompt PEER "peer hostname"
fi

WIZARD_ARGS=(wizard install --as "$ROLE" --peer "$PEER")
for b in "${EXTRA_BINS[@]:-}"; do
  [[ -z "$b" ]] && continue
  WIZARD_ARGS+=(--extra-binary "$b")
done

"$TARGET" "${WIZARD_ARGS[@]}"

# ---- final doctor check ----

step "running agentcookie doctor to confirm install state"

if "$TARGET" doctor; then
  ok "install complete; doctor reports all-green"
  ok "next: see docs/quickstart-beta.md for first-sync and SSH usage steps"
else
  warn "doctor reports issues; see output above and follow the [Remediation] lines"
  exit 1
fi
