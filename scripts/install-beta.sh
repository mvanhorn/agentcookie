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
#   --code <code>              [sink] Pairing code printed by the source's
#                              wizard install. Forwarded to wizard install.
#   --pair-url <url>           [sink] Source's pairing URL (e.g.
#                              http://<source>:9998/pair). Forwarded to wizard install.
#   --skip-keychain-prompt     [sink] Forwarded to wizard install. Auto-set
#                              when no TTY is attached (e.g. SSH non-pty).
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
CODE=""
PAIR_URL=""
SKIP_KEYCHAIN_PROMPT=""
EXTRA_WIZARD_ARGS=()
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
    --code)
      CODE="$2"; shift 2 ;;
    --pair-url)
      PAIR_URL="$2"; shift 2 ;;
    --skip-keychain-prompt)
      SKIP_KEYCHAIN_PROMPT="1"; shift ;;
    --skip-chrome-sqlite)
      EXTRA_WIZARD_ARGS+=("--skip-chrome-sqlite"); shift ;;
    --write-chrome-sqlite)
      EXTRA_WIZARD_ARGS+=("--write-chrome-sqlite"); shift ;;
    --no-cdp)
      EXTRA_WIZARD_ARGS+=("--no-cdp"); shift ;;
    --extra-binary)
      EXTRA_BINS+=("$2"); shift 2 ;;
    --bin-dir)
      BIN_DIR="$2"; shift 2 ;;
    --tarball)
      TARBALL="$2"; shift 2 ;;
    -h|--help)
      sed -n '1,35p' "$0" >&2
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
# The release tarball wraps everything in a versioned directory
# (agentcookie-${VERSION}-darwin-arm64/), so the binary is one level
# deep. find tolerates both shapes (wrapped + flat).
NEW_BIN="$(find "$WORK" -name agentcookie -type f -perm -u+x 2>/dev/null | head -n1)"
if [[ -z "$NEW_BIN" || ! -x "$NEW_BIN" ]]; then
  die "agentcookie binary not found inside tarball ($TARBALL)"
fi

step "verifying code signature"
# spctl -a is the wrong tool for CLI binaries (it assesses for app
# bundles and reports "rejected: not an app" even when the binary is
# correctly signed + notarized). Use codesign + Developer ID OU check
# instead.
if codesign --verify --strict --verbose=2 "$NEW_BIN" >/dev/null 2>&1; then
  if codesign -d -r- "$NEW_BIN" 2>&1 | grep -q "subject.OU. = NM8VT393AR"; then
    ok "binary is signed with the agentcookie Developer ID (NM8VT393AR)"
  else
    warn "binary signature is valid but Developer ID OU does not match NM8VT393AR"
    warn "continuing; this binary may be from a fork or an unofficial build"
  fi
else
  warn "codesign verification failed; LaunchAgent launches may be blocked by Gatekeeper. Continuing anyway."
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
  warn "$BIN_DIR is not on your \$PATH. The LaunchAgent uses absolute paths"
  warn "and will work fine, but \`agentcookie\` from a shell will not. To fix,"
  warn "add this line to your shell profile (~/.zshrc on macOS default):"
  warn "    export PATH=\"$BIN_DIR:\$PATH\""
  warn "Then run \`exec \$SHELL -l\` to reload."
fi

# ---- run wizard ----

step "running agentcookie wizard install --as $ROLE"

if [[ -z "$PEER" ]]; then
  echo "    What is the Tailscale hostname of the OTHER machine?"
  echo "    Run 'tailscale status' to list your tailnet hosts."
  prompt PEER "peer hostname"
fi

# Sink-only: collect the pair code and pair URL from the source's
# wizard install output. Both are required (the wizard refuses to
# start without them) so prompt if not passed.
if [[ "$ROLE" == "sink" ]]; then
  if [[ -z "$CODE" ]]; then
    echo "    Paste the pairing code printed by the source's wizard install"
    echo "    (looks like 'XXXX-YYYY-ZZZZ'):"
    prompt CODE "pair code"
  fi
  if [[ -z "$PAIR_URL" ]]; then
    echo "    Paste the pair URL printed by the source's wizard install"
    echo "    (looks like 'http://<source-host>:9998/pair'):"
    prompt PAIR_URL "pair URL"
  fi
fi

WIZARD_ARGS=(wizard install --as "$ROLE" --peer "$PEER")
if [[ "$ROLE" == "sink" ]]; then
  WIZARD_ARGS+=(--code "$CODE" --pair-url "$PAIR_URL")
fi
for b in "${EXTRA_BINS[@]:-}"; do
  [[ -z "$b" ]] && continue
  WIZARD_ARGS+=(--extra-binary "$b")
done
# v0.12.0-beta.3: forward --skip-chrome-sqlite, --write-chrome-sqlite,
# and --no-cdp explicitly if the operator passed them. The wizard
# itself auto-detects headless context when none are passed.
if [[ ${#EXTRA_WIZARD_ARGS[@]} -gt 0 ]]; then
  WIZARD_ARGS+=("${EXTRA_WIZARD_ARGS[@]}")
fi

# v0.12.0-beta.3: when there's no controlling TTY on a sink install,
# the wizard now auto-detects headless context and writes
# skip_chrome_sqlite + cdp.enabled into sink.yaml. No GUI Keychain
# prompt fires (the wizard skips the prompt step too, mirroring the
# v0.12.0-beta.2 behavior). The "Screen Share to click Always Allow"
# step is no longer required for the install to complete.
#
# Operators on a GUI session see the legacy default and can opt into
# headless mode explicitly with --skip-chrome-sqlite.
if [[ -z "$SKIP_KEYCHAIN_PROMPT" ]] && [[ "$ROLE" == "sink" ]] && ! [[ -t 0 ]]; then
  warn "no TTY detected; defaulting headless sink install."
  warn "  - sink.yaml will set skip_chrome_sqlite: true and cdp.enabled: true"
  warn "  - sink daemon will NOT read Chrome Safe Storage"
  warn "  - CDP injection will push cookies into ~/.agentcookie/chrome-profile each sync"
  warn "  - --skip-keychain-prompt is added to the wizard call so it does not block on GUI prompts"
  SKIP_KEYCHAIN_PROMPT="1"
fi
if [[ -n "$SKIP_KEYCHAIN_PROMPT" ]]; then
  WIZARD_ARGS+=(--skip-keychain-prompt)
fi

"$TARGET" "${WIZARD_ARGS[@]}"

# ---- final doctor check ----

step "running agentcookie doctor to confirm install state"

DOCTOR_EXIT=0
"$TARGET" doctor || DOCTOR_EXIT=$?

# ---- next steps hint (sink role only) ----
#
# A common friend pitfall after install: they SSH into the sink, type
# `instacart-pp-cli carts` (the example from quickstart-beta.md), and
# get `command not found`. agentcookie itself ships independent of the
# PP CLIs that consume its cookies; the friend has to install at least
# one PP CLI on the sink for the headline value to materialize. Make
# that step impossible to miss.
if [[ "$ROLE" == "sink" ]]; then
  echo
  echo "==============================================================="
  echo "  Next step: install at least one PP CLI on this sink."
  echo "==============================================================="
  echo
  echo "  agentcookie syncs cookies; the PP CLIs are what consume them."
  echo "  The five built-in adapters and their go install commands:"
  echo
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/printing-press-library/instacart-pp-cli@latest"
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/printing-press-library/airbnb-pp-cli@latest"
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/printing-press-library/ebay-pp-cli@latest"
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/printing-press-library/pagliacci-pp-cli@latest"
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/printing-press-library/table-reservation-goat-pp-cli@latest"
  echo
  echo "  Pick the ones you care about. After install, verify over SSH:"
  echo
  echo "    ssh $(hostname -s) 'instacart-pp-cli carts'"
  echo
  echo "  PP CLIs reading cookies via:"
  echo "    - adapter session files (v0.11) -- auto-populated by sink"
  echo "    - sidecar (v0.8) -- set AGENTCOOKIE_PLAIN_COOKIES=~/.agentcookie/cookies-plain.db"
  echo "==============================================================="
  echo
fi

if [[ $DOCTOR_EXIT -eq 0 ]]; then
  ok "install complete; doctor reports all-green"
  ok "next: install one or more PP CLIs (above) and verify over SSH"
else
  warn "doctor reports issues; see output above and follow the [Remediation] lines"
  exit 1
fi
