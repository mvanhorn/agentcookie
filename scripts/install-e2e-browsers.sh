#!/usr/bin/env bash
# CI-only fixture downloads. Fixed official URLs and observed SHA-256 pins.
# Does not launch a browser, install a service, or inspect a user's profile.
set -euo pipefail
mode=${1:?usage: install-e2e-browsers.sh linux|macos|macos-chrome destination}
root=${2:?fixture destination required}
mkdir -p "$root"
root=$(cd "$root" && pwd)
fetch() {
  local url=$1 hash=$2 output=$3
  curl --fail --location --retry 3 --proto '=https' --tlsv1.2 "$url" -o "$output"
  printf '%s  %s\n' "$hash" "$output" | shasum -a 256 -c -
}
emit() { printf '%s=%s\n' "$1" "$2"; if [[ -n ${GITHUB_ENV:-} ]]; then printf '%s=%s\n' "$1" "$2" >> "$GITHUB_ENV"; fi; }
case "$mode" in
 linux)
  [[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo 'Linux x86_64 fixtures required' >&2; exit 1; }
  fetch 'https://storage.googleapis.com/chrome-for-testing-public/157.0.8103.50/linux64/chrome-linux64.zip' d17167bc092eb495a3b768c7467f27e35f625aa03834233a1663af67dcb668c9 "$root/chrome.zip"
  unzip -q "$root/chrome.zip" -d "$root"
  emit AGENTCOOKIE_CHROME_BINARY "$root/chrome-linux64/chrome"
  fetch 'https://archive.mozilla.org/pub/firefox/releases/160.0/linux-x86_64/en-US/firefox-160.0.tar.xz' fde774e9757c8878534c05ec210db592bf6a1b244c57941ffb2e364c04f35fcd "$root/firefox.tar.xz"
  tar -xJf "$root/firefox.tar.xz" -C "$root"
  emit AGENTCOOKIE_GECKO_BINARY "$root/firefox/firefox"
  fetch 'https://github.com/mvanhorn/agentcookie/releases/download/v1.7.0/agentcookie_1.7.0_linux_amd64.tar.gz' 7e3f07a9fb6324fed830a0b11bc2c4bc44c2b3e84dbcb931ed777d22ad96f3c9 "$root/previous.tar.gz"
  mkdir "$root/previous"
  tar -xzf "$root/previous.tar.gz" -C "$root/previous"
  test -x "$root/previous/agentcookie"
  emit AGENTCOOKIE_OLD_BINARY "$root/previous/agentcookie"
  ;;
 macos|macos-chrome)
  [[ $(uname -s) == Darwin ]] || { echo 'macOS fixtures required' >&2; exit 1; }
  case $(uname -m) in
   arm64) platform=mac-arm64; hash=aceb0578a1cc3631358b164dbbef04bf8936f96c690fb78c3ac25b7655e795e8 ;;
   x86_64) platform=mac-x64; hash=7b1c12bee8024f1193b28754aa0d46b7eaa08394de51bef84fd65a6cf08d61dc ;;
   *) exit 1 ;;
  esac
  fetch "https://storage.googleapis.com/chrome-for-testing-public/157.0.8103.50/$platform/chrome-$platform.zip" "$hash" "$root/chrome.zip"
  unzip -q "$root/chrome.zip" -d "$root"
  emit AGENTCOOKIE_CHROME_BINARY "$root/chrome-$platform/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"
  if [[ "$mode" == macos-chrome ]]; then exit 0; fi
  fetch 'https://github.com/zen-browser/desktop/releases/download/1.24.1b/zen.macos-universal.dmg' 00194b221ae87c76d82a8b4af5bdd89d49100bed78c6aa0df754a75de7733f8a "$root/zen.dmg"
  mount="$root/zen-mounted"
  mkdir "$mount"
  hdiutil attach "$root/zen.dmg" -readonly -nobrowse -mountpoint "$mount"
  trap 'hdiutil detach "$mount" >/dev/null 2>&1 || true' EXIT
  test -d "$mount/Zen.app"
  ditto "$mount/Zen.app" "$root/Zen.app"
  hdiutil detach "$mount"
  trap - EXIT
  emit AGENTCOOKIE_GECKO_BINARY "$root/Zen.app/Contents/MacOS/zen"
  # Deliberately no old release download or execution on macOS.
  ;;
 *) echo 'expected linux, macos or macos-chrome' >&2; exit 1 ;;
esac
