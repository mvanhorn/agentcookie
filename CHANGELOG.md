# Changelog

## [Unreleased]

### v0.10: one-time keychain access for headless kooky CLIs

The remaining gap after v0.9: kooky-using CLIs (instacart-pp-cli, bird,
future PP CLIs) hit a macOS Keychain prompt the first time each binary
tried to read Chrome Safe Storage. v0.10 closes the gap with a single
wizard step at sink install time.

`agentcookie wizard install --as sink` now auto-runs
`set-keychain-access`, which spawns a one-shot LaunchAgent in the
user's GUI session and iterates strategies to broaden Chrome Safe
Storage access. The primary strategy is `delete-and-recreate-with-A`:
remove the existing keychain item, recreate with `security
add-generic-password -A` and a fresh random password. On a fresh
install this works without a login keychain password prompt because
`SecKeychainAddGenericPassword` on a new item honors `-A` at creation
time. Each strategy is validated via the same Apple Security framework
API path kooky-CGO uses (`github.com/keybase/go-keychain.GetGenericPassword`,
exposed as `agentcookie internal keychain-probe`).

For sinks where the keychain item already exists with a restrictive
ACL from a prior Chrome run or earlier agentcookie install, the
strategy may fail; the install does not abort, prints a loud warning,
and points at `docs/runbook-v0.10-keychain-access.md`. The runbook
documents a one-time GUI fallback (open Keychain Access, click
"Allow all applications to access this item" on the Chrome Safe Storage
row, type your login password once) that clears the prior ACL state so
a wizard re-run succeeds.

`chrome.SafeStoragePassword` now tries the keybase/go-keychain CGO path
first and falls back to shelling out to the `security` CLI. The keybase
path is more reliable from LaunchAgent contexts where the legacy
`security` CLI sometimes returns exit 44 with empty stdout despite the
underlying keychain being readable.

Validated live on a Mac mini: after one-time setup,
`ssh matts-mac-mini 'instacart-pp-cli auth login'` returns
`imported N cookies from Chrome` and exit 0 with no GUI prompt;
`instacart-pp-cli doctor` reports `[ok] session: N cookies from chrome`
(not "from paste") and `[ok] api: logged in as`. End-to-end runbook
at `docs/runbook-v0.10-keychain-access.md`.

Security trade-off: Chrome Safe Storage is now any-app-readable on
sink machines. The practical threat model already assumes a sink-side
process compromise means lost cookies (the plaintext sidecar at
`~/.agentcookie/cookies-plain.db` is there too). Pass
`--skip-keychain-access` to the wizard to opt out of the broader ACL.

### v0.9: plain v10 sink writes for headless kooky readers

agentcookie's sink-side write now emits Chrome cookies in plain v10
format with no App-Bound 32-byte plaintext prefix, and pins
`meta.version = 18` in the cookies SQLite. The effect: PP CLIs and any
other kooky v0.2.2 caller on the Mac mini can read the file directly
without per-CLI cooperation, App-Bound knowledge, or paste-from-laptop
ceremony. See `docs/plans/2026-05-17-003-feat-agentcookie-v10-mode-soup-to-nuts-plan.md`.

Precondition: Mac mini Chrome stays quit during agent operation.
Launching it would migrate `meta.version` to 24 and rewrite cookies in
App-Bound v20, breaking every kooky v0.2.2 reader. The sink uses
`chromectl.WithChromeDown` (not WithChromeQuit) so writes never trigger
a relaunch.

The install wizard now expands the Chrome Safe Storage Keychain
partition list during sink install so Apple-tool callers read the
password without GUI prompts. Ad-hoc-signed Go binaries (most PP CLIs)
still need a one-time "Always Allow" click on first read; the partition
list is groundwork.

Each sink write runs a post-commit probe that decrypts a few cookies the
way kooky v0.2.2 would and logs `probe ok` or `probe FAIL` with
diagnostic counts. A regression of either the App-Bound write or the
meta.version pin surfaces in stderr immediately instead of corrupting
agent runs silently.

Deferred for a future coordinated bump: switch back to App-Bound v20
mode once PP CLIs and the printing-press meta-library move from kooky
v0.2.2 to v0.2.9+ (which strips the 32-byte prefix when `dbVersion >= 24`).
Until that bump, v0.9 plain-v10 mode is the bridge.

End-to-end runbook: `docs/runbook-v0.9-soup-to-nuts.md`.

## [Unreleased pre-v0.9]

Tag v0.1.0 to cut the first release. See [docs/quickstart.md](docs/quickstart.md) to install and try it.

### Added (since project start)

- Unified `agentcookie` CLI with subcommands `source`, `sink`, `pair`, `status`, `version`. All support `--json` for agent callers.
- Pairing handshake: X25519 + HKDF-SHA256 salted with a one-time code. Derived 32-byte keys land in `~/.config/agentcookie/keys/<peer>.json` at mode 0600.
- Cookie acquisition on macOS Chrome: read SQLite read-only with `immutable=1`, decrypt v10 ciphertext with the Keychain Safe Storage key.
- Cookie write: schema-aware `INSERT ... ON CONFLICT` that adapts to Chrome's evolving column set.
- Live-Chrome cookie injection on the sink via Chrome DevTools Protocol (`Storage.setCookies`). Falls back to SQLite write when Chrome is not debuggable.
- Wire protocol v1: versioned `SyncEnvelope` with monotonic Sequence (in-memory replay defense), source hostname, cookies. Documented at `docs/protocol.md`.
- Sink-side allowlist enforcement, independent of source's allowlist.
- Install skill at `skill/SKILL.md` for Claude Code / gstack-style installer flows.
- launchd plist template for unattended sink operation.
- Marketing site at `web/index.html`, ready to deploy to any static host.
- 42 unit tests across the chrome, transport, config, keystore, pairing, CDP, and protocol packages.
- Apache 2.0 license.

### Not yet shipped (planned for v0.2)

- macOS Keychain storage for paired keys.
- Long-lived fsnotify-driven watch mode on the source (replacing the current `--once` + cron pattern).
- Durable replay defense (nonce or timestamp window in the envelope).
- `agentcookie pair --rotate` for live key rotation.
- One-to-many fan-out (one source pushing to multiple sinks).
- Linux sink support.
