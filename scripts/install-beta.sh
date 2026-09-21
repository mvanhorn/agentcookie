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
#                              the newest macOS release asset.
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

# ---- release asset selection ----
# Sourced by scripts/release_asset_test.sh. Kept inside this file because
# the release tarball ships install-beta.sh on its own.

# macos_host_arch [UNAME_M]
# Map `uname -m` onto the asset tokens we publish. x86_64 and amd64 are
# the same Intel slice (Go's amd64, lipo's x86_64).
macos_host_arch() {
  local machine="${1:-$(uname -m)}"
  case "$machine" in
    arm64|aarch64) printf 'arm64\n' ;;
    x86_64|amd64) printf 'amd64\n' ;;
    *) return 1 ;;
  esac
}

# macos_release_jq HOST
# jq program for one page of GET /repos/{owner}/{repo}/releases.
# HOST is arm64 or amd64. Emits TSV rows, newest published_at first:
#   prerelease<TAB>tag<TAB>asset
# asset is empty when that release has no usable macOS tarball.
# A usable asset is a darwin universal archive, otherwise a tarball for
# HOST. gh's --pattern glob does not support character classes, so the
# caller downloads the exact asset name this filter returns.
macos_release_jq() {
  local host="$1"
  case "$host" in
    arm64|amd64) ;;
    *) return 1 ;;
  esac
  sed "s/__HOST__/${host}/g" <<'JQ'
def is_tarball:
  ((.name // "") | test("\\.tar\\.gz$"));
def is_universal:
  ((.name // "") | test("darwin.*universal"));
def is_host_arch:
  if "__HOST__" == "arm64" then
    ((.name // "") | test("darwin[-_]arm64"))
  else
    (((.name // "") | test("darwin[-_]amd64")) or ((.name // "") | test("darwin[-_]x86_64")))
  end;
def chosen_asset:
  . as $assets
  | ($assets | map(select(is_tarball and is_universal))) as $uni
  | ($assets | map(select(is_tarball and is_host_arch and (is_universal | not)))) as $arch
  | if ($uni | length) > 0 then $uni[0].name
    elif ($arch | length) > 0 then $arch[0].name
    else ""
    end;
map(select(.draft | not))
| sort_by(.published_at // "")
| reverse
| .[]
| [
    (if .prerelease then "true" else "false" end),
    .tag_name,
    ((.assets // []) | chosen_asset)
  ]
| @tsv
JQ
}

# select_macos_release_from_rows TSV
# TSV is the concatenated output of macos_release_jq across release
# pages, already newest-first. Prefer the newest stable release with a
# usable asset; fall back to the newest prerelease only when no stable
# release has one. Prints:
#   tag<US>asset<US>used_prerelease<US>latest_stable
# US is ASCII unit separator. tag and asset are empty when nothing
# matched. latest_stable is the newest non-draft stable tag, even when
# it has no macOS asset. A non-whitespace separator keeps those empty
# fields; `read` with IFS=$'\t' would drop a leading tab.
select_macos_release_from_rows() {
  local rows="$1"
  local latest_stable="" release_tag="" asset_name="" pre_tag="" pre_asset=""
  local pre="" tag="" asset="" used_pre=0

  while IFS=$'\t' read -r pre tag asset || [[ -n "${tag:-}" ]]; do
    [[ -z "${tag:-}" ]] && continue
    if [[ "$pre" == "false" && -z "$latest_stable" ]]; then
      latest_stable="$tag"
    fi
    if [[ -n "${asset:-}" && "$pre" == "false" && -z "$release_tag" ]]; then
      release_tag="$tag"
      asset_name="$asset"
    elif [[ -n "${asset:-}" && "$pre" == "true" && -z "$pre_tag" ]]; then
      pre_tag="$tag"
      pre_asset="$asset"
    fi
  done <<< "$rows"

  if [[ -z "$release_tag" && -n "$pre_tag" ]]; then
    release_tag="$pre_tag"
    asset_name="$pre_asset"
    used_pre=1
  fi

  printf '%s\037%s\037%s\037%s\n' "$release_tag" "$asset_name" "$used_pre" "$latest_stable"
}

# asset_usable_for_host NAME HOST
# True when NAME is a darwin universal tarball, or a darwin tarball for
# HOST. Globs cover underscore and hyphen spellings. Never treats
# linux_*_arm64 as macOS.
asset_usable_for_host() {
  local name="$1" host="$2"
  local lower
  lower="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')"
  [[ "$lower" == *.tar.gz ]] || return 1
  [[ "$lower" == *darwin* ]] || return 1
  if [[ "$lower" == *darwin*universal* ]]; then
    return 0
  fi
  case "$host" in
    arm64)
      [[ "$lower" == *darwin_arm64* || "$lower" == *darwin-arm64* ]]
      ;;
    amd64)
      [[ "$lower" == *darwin_amd64* || "$lower" == *darwin-amd64* || "$lower" == *darwin_x86_64* || "$lower" == *darwin-x86_64* ]]
      ;;
    *)
      return 1
      ;;
  esac
}

# pick_downloaded_tarball DIR EXPECTED_NAME
# Print the path whose basename is EXPECTED_NAME. An empty glob is not
# an error under `set -euo pipefail` (nullglob). Does not use `ls | head`,
# which would pick darwin_amd64 ahead of darwin_arm64 by sort order.
pick_downloaded_tarball() {
  local dir="$1" expected="$2"
  local f base found="" nullglob_was=0
  local -a matches=()
  shopt -q nullglob && nullglob_was=1
  shopt -s nullglob
  matches=("$dir"/*.tar.gz)
  if [[ $nullglob_was -eq 0 ]]; then
    shopt -u nullglob
  fi
  if [[ ${#matches[@]} -eq 0 ]]; then
    echo "install-beta.sh: release tarball not found after download (looked in $dir)" >&2
    return 1
  fi
  for f in "${matches[@]}"; do
    base="$(basename "$f")"
    if [[ "$base" == "$expected" ]]; then
      found="$f"
      break
    fi
  done
  if [[ -z "$found" ]]; then
    echo "install-beta.sh: download did not include $expected (found: ${matches[*]})" >&2
    return 1
  fi
  printf '%s\n' "$found"
}

if [[ "${AGENTCOOKIE_INSTALL_BETA_LIB_ONLY:-}" == "1" ]]; then
  if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
  fi
  echo "install-beta.sh: AGENTCOOKIE_INSTALL_BETA_LIB_ONLY is set; selection helpers only, not installing" >&2
  exit 0
fi

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
  # The newest GitHub release is not guaranteed to have a macOS build
  # (v1.1.0 shipped linux-only). Asset names also changed at v1.0.0:
  #   <= v0.17.1   agentcookie-v0.17.1-darwin-arm64.tar.gz   (hyphens)
  #   >= v1.0.0    agentcookie_1.0.0_darwin_arm64.tar.gz     (underscores)
  #   universal    agentcookie_<version>_darwin_universal.tar.gz
  #                (darwin-universal is accepted too)
  # Walk non-draft releases newest-first. Prefer a universal archive,
  # otherwise the host architecture. Prefer a stable release over a
  # prerelease. Do not swallow gh failures: an API or download error
  # must not look like "no macOS release".
  if ! HOST_ARCH="$(macos_host_arch)"; then
    die "unsupported architecture $(uname -m). This installer needs arm64 or x86_64 to choose a macOS release asset."
  fi
  step "finding newest release with a macOS build for $HOST_ARCH"
  if ! RELEASE_ROWS="$(gh api "repos/$REPO/releases?per_page=100" --paginate --jq "$(macos_release_jq "$HOST_ARCH")")"; then
    die "GitHub API request failed while listing releases for $REPO. This is an API, auth, or network error, not a release that lacks a macOS build. Check 'gh auth status' and try again, or re-run with --tarball <path>."
  fi
  SELECT_OUT="$(select_macos_release_from_rows "$RELEASE_ROWS")"
  IFS=$'\037' read -r RELEASE_TAG ASSET_NAME USED_PRERELEASE LATEST_STABLE <<< "$SELECT_OUT"
  if [[ -z "$RELEASE_TAG" || -z "$ASSET_NAME" ]]; then
    die "no release in $REPO publishes a macOS asset this machine can run (darwin universal, or darwin ${HOST_ARCH}). Download a tarball manually and re-run with --tarball <path>."
  fi
  if ! asset_usable_for_host "$ASSET_NAME" "$HOST_ARCH"; then
    die "refusing to download $ASSET_NAME: it is not a universal macOS build or a ${HOST_ARCH} macOS build."
  fi
  if [[ -n "$LATEST_STABLE" && "$LATEST_STABLE" != "$RELEASE_TAG" ]]; then
    warn "latest release $LATEST_STABLE has no usable macOS build for this machine; using $RELEASE_TAG instead"
  fi
  if [[ "$USED_PRERELEASE" == "1" ]]; then
    warn "$RELEASE_TAG is a pre-release (no stable release has a usable macOS build for this machine)"
  fi
  ok "selected $RELEASE_TAG ($ASSET_NAME)"

  step "downloading $RELEASE_TAG from $REPO"
  TMP_DL="$(mktemp -d -t agentcookie-beta.XXXXXX)"
  # Exact asset name, not '*darwin*'. A bare darwin glob plus `ls | head`
  # picks darwin_amd64 ahead of darwin_arm64. gh --pattern has no
  # character classes, so the name chosen above is the pattern.
  if ! gh release download "$RELEASE_TAG" --repo "$REPO" --pattern "$ASSET_NAME" --dir "$TMP_DL" --clobber; then
    die "failed to download $ASSET_NAME from release $RELEASE_TAG. The release listing already found this asset, so this is a GitHub download error (auth, network, or rate limit), not a missing macOS build."
  fi
  if ! TARBALL="$(pick_downloaded_tarball "$TMP_DL" "$ASSET_NAME")"; then
    die "release tarball not found after download (looked in $TMP_DL for $ASSET_NAME). Re-run with --tarball <path> if you already have the archive."
  fi
  ok "downloaded $(basename "$TARBALL")"
fi

# ---- extract and verify binary ----

WORK="$(mktemp -d -t agentcookie-install.XXXXXX)"
tar -xzf "$TARBALL" -C "$WORK"
# The release tarball wraps everything in a versioned directory
# (agentcookie_<version>_darwin_universal/, or darwin_arm64 /
# darwin_amd64, plus older hyphenated names), so the binary is one
# level deep. find tolerates both shapes (wrapped + flat).
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
  echo "  Two of the five built-in-adapter PP CLIs are go-installable today:"
  echo
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/instacart-pp-cli@latest"
  echo "    GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/airbnb-vrbo-pp-cli@latest"
  echo
  echo "  The remaining three (ebay, pagliacci-pizza, table-reservation-goat)"
  echo "  ship via the printing-press meta tool; see"
  echo "    https://github.com/mvanhorn/printing-press-library"
  echo
  echo "  After installing instacart-pp-cli, verify over SSH from your laptop:"
  echo
  echo "    ssh $(hostname -s) 'instacart-pp-cli carts'"
  echo
  echo "  Cookie delivery paths to PP CLIs (no env vars needed for the five"
  echo "  with built-in adapters; sidecar env var is the fallback):"
  echo "    - adapter session files (v0.11) -- auto-populated by the sink"
  echo "    - sidecar -- export AGENTCOOKIE_PLAIN_COOKIES=~/.agentcookie/cookies-plain.db"
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
