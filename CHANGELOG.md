# Changelog

## [Unreleased]

### v0.12: security hardening (sealed master key, tailnet-only listeners, rate-limited pairing, sealed sidecar + adapter files)

A friend with a security background looked at agentcookie after v0.11
and called it a nightmare. A code-grounded threat survey confirmed
it: v0.10 and v0.11 silently expanded the sink's attack surface in
ways the threat-model doc never documented. On a sink running
v0.10 + v0.11, every cookie value on every synced domain, every
per-CLI session token for every adapter, and the Chrome Safe Storage
AES key itself were readable by any process running as the user,
while the listener was on every network interface by default and the
pairing endpoint accepted unlimited guesses against a 40-bit code.

v0.12 closes that picture without adding a single new prompt in
steady-state operation. The wizard install ceremony stays one
Keychain unlock; everything else happens headlessly forever after.

Shipped:

- Apple Developer ID signing (U0). Every agentcookie binary is
  signed with a stable Developer ID, hardened-runtime + timestamped.
  Stable designated requirement across rebuilds is the property the
  rest of the work depends on.
- Tailnet-only listeners (U1). `agentcookie sink` and the source
  pair listener refuse to start on `0.0.0.0` or any non-Tailscale
  interface. Wizard install fails loud if the Tailscale 100.x
  interface is missing rather than silently falling back.
- HTTP server + client timeouts and body caps (U2 + U11 + U14). One
  `internal/cli/httpserver` helper defines the policy. Sink and pair
  listeners get ReadHeaderTimeout / ReadTimeout / WriteTimeout /
  MaxBodyBytes. The pairing client gets a 30-second timeout.
- Persistent replay defense + nanosecond sequence (U3). Sink restart
  no longer opens a one-shot replay window. Rapid syncs within the
  same second no longer collide.
- Hardened pair endpoint (U4). Pair code bumped from 8 base32 chars
  (40 bits) to 12 (64 bits). Per-IP token bucket caps wrong-code
  attempts (5 before 429, 500ms refill).
- Sealed master key (U5). New `agentcookie-master` Keychain item
  protected by a per-binary `-T` ACL that names the Developer-ID-
  signed agentcookie binary plus each adapter binary. Replaces
  v0.10's `-A` ACL on Chrome Safe Storage with the same list. Any
  non-allowlisted user process can no longer silently read Chrome's
  cookie-encryption key.
- Sealed cookie sidecar (U6). When the master key is available, the
  sink seals each cookie value in `~/.agentcookie/cookies-plain.db`
  before write. New `pkg/sidecar.ReadSidecar` is the public API PP
  CLIs link.
- Sealed adapter session files (U7). Pycookiecheat-style adapters
  (Airbnb, eBay, Pagliacci) and the table-reservation adapter
  (OpenTable, Tock) seal their secret-bearing fields. Plaintext
  fallback when no master key is present preserves partial-install
  paths.
- Cookie input validation (U8). Names, values, and host_keys flowing
  through adapters pass an RFC 6265 token + control-char validator.
  Drops surface in `wizard verify-adapters` as the new `Invalid`
  count. Fixes the unanchored host-suffix bug that matched
  `xopentable.com` for the OpenTable filter.
- Tarball unpack hardening (U9). Sink rejects LocalStorage /
  IndexedDB tarballs over 256 MB, with more than 100,000 members,
  or containing `..` / absolute-path / symlink / hardlink entries.
- Legacy shared_secret entropy floor + drop SHA-256 double-hash
  (U10). Pairing-derived 32-byte keys pass directly through to the
  AES-256-GCM cipher; legacy free-form `security.shared_secret`
  values below 32 bytes are now refused at config load.

Pending follow-up:

- U12: PP CLI sidecar-reader migration in cli-printing-press. Each
  of the five built-in adapter PP CLIs gains a small import of
  `pkg/sidecar` so it reads sealed session caches transparently.
  v0.12 ships the writer side and the public reader API; the PP CLI
  consumer-side change tracks in cli-printing-press. Until that
  migration lands, PP CLIs continue to work against v0.11-shape
  plaintext sidecars (the sink falls back when the master key
  Keychain item is absent).

### v0.11: sinkpush adapter pushes cookies into each PP CLI's session cache

The product UX gap after v0.10: each new PP CLI on the Mac mini
triggered a fresh Keychain Always-Allow prompt the first time it
read Chrome Safe Storage. macOS's modern SecItem API (used by both
Python keyring and keybase/go-keychain) does not durably honor the
legacy `-A` ACL or `-T` trust-list entries for ad-hoc-signed Go
binaries. Multi-click NUX was unacceptable.

v0.11 closes the gap by side-stepping Keychain on the CLI side
entirely. The sink already has stable Keychain access in its
LaunchAgent context. After each successful WriteCookies commit, the
new `internal/sinkpush` package iterates a registered set of PP CLI
adapters; each adapter takes the cookies it cares about (host-pattern
filter) and writes them directly into that CLI's local session
cache. PP CLIs read their own session files on subsequent
invocations -- they never touch Chrome cookies or Keychain.

Five built-in adapters ship with v0.11:

- `instacart-pp-cli` -- shells out to `instacart auth paste` with a
  Cookie header on stdin (canonicalizes the existing
  `hack/dump-instacart` flow).
- `airbnb-pp-cli`, `ebay-pp-cli`, `pagliacci-pp-cli` -- shared
  `PycookiecheatStyleAdapter` writes `~/.config/<cli>/config.toml`
  (access_token field) and `cookies.json` (cookies field). Patches
  existing config.toml in place to preserve user-set base_url and
  other fields; creates from a canonical template on fresh installs.
- `table-reservation-goat-pp-cli` -- writes structured cookie objects
  into `~/.config/.../session.json` split across opentable_cookies
  and tock_cookies arrays.

Per-adapter failures are loud (logged to sink stderr, recorded in
`~/.agentcookie/sink-state.json`) but non-fatal. The cookie write
and sidecar paths run first and are the source of truth.

New surfaces:

- `agentcookie wizard verify-adapters` prints a table of the most
  recent adapter run; --json envelope for SSH agents.
- `agentcookie status` gains a one-line adapter rollup under the
  sink-daemon section.
- `state.SinkState.LastAdapterResults` records per-adapter outcome
  for the most recent sync.

End-to-end runbook: `docs/runbook-v0.11-adapter-cookie-push.md`.
Architectural rationale (why this beats Keychain ACL manipulation
on macOS 15+) lives in that runbook and in plan
`docs/plans/2026-05-17-007-feat-sink-cli-adapter-cookie-push-plan.md`.

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
