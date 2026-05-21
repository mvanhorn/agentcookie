# Changelog

## [Unreleased]

### v0.12.0-beta.4: CDP injection coverage fix + PP CLI install hint

The 2026-05-21 dry-run shipped v0.12.0-beta.3 with a headline that
mostly worked but missed the actual sites a friend cares about. Two
findings, both fixed here:

**CDP injection drop rate.** The `cdp.InjectCookies` call was passing
Domain+Path-only `CookieParam` records to `Network.setCookies`. Chrome
applies stricter validation when no URL is provided -- rejecting
SameSite=None without Secure, missing-SameSite defaults to Lax which
rejects originally cross-site cookies, and host-only/subdomain
semantics flake. The dry-run measured a 64% global drop rate and
90%+ on instacart.com.

Fix: synthesize a URL per cookie from `host_key` + `path` + scheme
(strip leading dot for the hostname), pass it alongside Domain+Path,
and translate Chrome's numeric SameSite encoding to the CDP enum
explicitly. Tests cover all four SameSite values and the URL
synthesis edge cases. Pre-beta.4 the build also dropped Priority and
SourceScheme; those stay omitted (cdproto defaults are acceptable),
but the CookieParam now reflects the full intent.

**PP CLI install hint.** `agentcookie` syncs cookies but the headline
value comes from the PP CLIs that consume them. install-beta.sh used
to land + return without telling the friend they still need to
`go install` at least one PP CLI on the sink. Result: friend SSHs in,
runs `instacart-pp-cli carts`, gets `command not found`, thinks
agentcookie is broken.

Fix: install-beta.sh now prints a clear post-install block listing
the five built-in adapters' `go install` commands and an SSH-test
verification line. quickstart-beta.md gains a new "Install at least
one PP CLI on the sink" section between the sink install and the
verify steps.

### v0.12.0-beta.3: click-free headless sink (skip Chrome SQLite write + CDP injection)

The dominant blocker in the 2026-05-19 first-friend dry-run was the
Chrome Safe Storage Keychain prompt. The sink daemon needed Chrome's
per-machine AES key to encrypt cookies before writing Chrome's SQLite,
and macOS only grants that access via a GUI "Always Allow" click. An
SSH-only install on a headless Mac mini had no one to click it.

v0.12.0-beta.3 closes that gap with two phases working together:

**Phase 1 — Skip Chrome SQLite write on headless sinks.**

- New `skip_chrome_sqlite: true` in `sink.yaml` makes the sink daemon
  never read Chrome Safe Storage and never write Chrome SQLite,
  LocalStorage, or IndexedDB. The plaintext-cookies sidecar
  (`~/.agentcookie/cookies-plain.db`, pair-derived shared key) and the
  v0.11 adapter push (per-PP-CLI session files) remain the
  cookie-delivery paths. PP CLIs are unaffected.
- `agentcookie wizard install --as sink` auto-detects no-TTY contexts
  (the SSH-only install path) and writes `skip_chrome_sqlite: true`
  by default. GUI installs (you're at the sink's keyboard) keep the
  legacy behavior. Explicit `--skip-chrome-sqlite` and
  `--write-chrome-sqlite` flags override the auto-detect.
- `install-beta.sh` forwards the new flags and surfaces the new
  default in its post-install hint.
- `agentcookie doctor` now reports the active write mode in the Sink
  state check (`mode=sidecar+adapter` vs `mode=sqlite+leveldb`) and
  warns when sidecar cookie domains have no matching adapter (a new
  "Adapter coverage" check, WARN severity).

**Phase 2 — CDP injection keeps Chrome on the sink warm.**

- New `cdp.enabled: true` in `sink.yaml` makes the sink launch a
  one-shot headless Chrome via chromedp after each /sync and push
  the synced cookies through `Storage.setCookies`. Chrome handles
  its own Safe Storage encryption; agentcookie never reads Chrome's
  Keychain item.
- Chrome 127+ App-Bound Encryption: the CDP path now strips the
  32-byte host-bound prefix from decrypted cookie values before
  calling `Storage.setCookies`. Closes #10. The SQLite write path is
  unchanged (Chrome strips the prefix itself on read).
- The CDP-targeted profile lives at `~/.agentcookie/chrome-profile/`
  — agentcookie-owned, separate from the friend's default Chrome
  profile. Launching Chrome.app on the sink against this profile
  shows synced sites already logged in.
- Wizard auto-enables CDP when it auto-enables headless mode.
  `--no-cdp` opts out for friends who want sidecar+adapter only.
- `agentcookie doctor` adds a "CDP injector" check that verifies the
  profile dir exists and Chrome.app is installed.

**chromedp added as a dependency.** ~50K LOC vendored. Pinned to
v0.15.1.

**Backward compatibility (R6).** A v0.12.0-beta.2 sink.yaml that does
not mention `skip_chrome_sqlite` or `cdp` keeps the legacy
chrome-sqlite write path verbatim. Installed friends upgrading the
binary in place see no behavior change.

Shipped under plan `docs/plans/2026-05-21-001-feat-headless-sink-click-free-plan.md`.

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

Sealing posture in v0.12: shipped but off by default.

The at-rest sealing for the sidecar (U6) and adapter session files
(U7) is wired into the writers but the wizard install does NOT
create the `agentcookie-master` Keychain item by default. The PP
CLI consumer side of the sealing handshake (U12, tracked in
cli-printing-press) has not shipped yet; turning sealing on
without that release would break v0.11 PP CLIs that read plaintext
sidecars and adapter session files.

To opt in once the matching cli-printing-press release lands:

```
agentcookie wizard set-keychain-access --enable-sealing
```

Threat-survey finding S5 (plaintext cookie sidecar at rest) stays
open in the default install. Operators who only run agentcookie-
controlled binaries on the sink can opt in immediately and close
S5 themselves; the rest wait on U12. Chrome Safe Storage's `-T`
ACL (replacing v0.10's any-app `-A`) is installed in both modes;
only the master key step is gated.

Pending follow-up:

- U12: PP CLI sidecar-reader migration in cli-printing-press. Each
  of the five built-in adapter PP CLIs gains a small import of
  `pkg/sidecar` so it reads sealed session caches transparently.
  Unblocks flipping `--enable-sealing` to the default in a future
  agentcookie release.

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
