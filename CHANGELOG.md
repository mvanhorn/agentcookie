# Changelog

## [Unreleased]

### Source: `--skip-local-storage` stops shipping Local Storage (#150, by @Dannyxlm; reported in #149)

- Every push packs the source profile's Local Storage leveldb into the envelope, base64-encoded, and the watcher pushes as often as every 30 seconds. On one Mac the directory was 64 MB, so each push was about 90 MB and the laptop uploaded about 150 GB in 16.5 hours to a Linux sink that never applies Local Storage. On slower uplinks the push also overran the watcher's 60-second budget (`context deadline exceeded`), so cookies stopped syncing too.
- New `agentcookie source --skip-local-storage` (or `AGENTCOOKIE_SKIP_LOCAL_STORAGE=1`, for a LaunchAgent) skips the pack and stops watching the Local Storage directory. On that Mac each push dropped to about 1.8 MB, with the same cookies delivered. Without the flag nothing changes. No `source.yaml` key is added, so older binaries still load the config, and the wire format is unchanged (the tarball field is just empty).

### Source: `--skip-secrets` runs a cookie-only source

- New `agentcookie source --skip-secrets` (or `AGENTCOOKIE_SKIP_SECRETS`, for a LaunchAgent) stops the source from reading the secrets bus at all: no manifests, per-CLI secrets or carried files, in `--once` and `--watch`. Pushes to sinks and the `GET /pull` envelope for teammates carry no secrets, and `--watch` no longer watches the secrets and discovery paths. The env var fails closed: any value other than empty or `0` turns it on, and a value other than `1` also prints a warning naming it. With `--skip-local-storage`, this gives a cookie-only source.
- `agentcookie doctor` adds a Secrets push line: "skipped (--skip-secrets or AGENTCOOKIE_SKIP_SECRETS)" or "shipping N cli(s)" for the last push. It is informational and never warns.
- The switch stops future sends only. Secrets already delivered stay on sinks and teammates until removed there.
- Requested by @jnyross in #151.

### Linux sinks honor `live_cdp.enabled: false`

- On Linux, the sink turned live CDP injection on whenever `cdp.enabled` was off, even when `sink.yaml` set `live_cdp: enabled: false`. An explicit `live_cdp.enabled` is now kept as written. Live CDP still defaults to on when the key is absent and `cdp.enabled` is off. `skip_chrome_sqlite` stays forced on for Linux sinks, and the code comment that said YAML could turn it off now says it cannot. Reported by @Dannyxlm in #149.

### Source push cadence on slow links

- `source --watch` now gives each push the same time budget as `source --once` (the sync client's per-sink timeout plus 30 seconds) instead of a fixed 60 seconds, so large payloads on slow uplinks stop timing out. A trigger that arrives while a push is still running is skipped instead of queuing behind it; the next baseline tick sends the current state. `cmux-sync` and `agent-sync` keep the 60 second budget. Reported by @Dannyxlm in #149.
- `source --watch` no longer re-sends an unchanged payload every 30 seconds. Each sink is sent the payload only when it changed since that sink last accepted it, or as a heartbeat once 10 minutes have passed. A failed push is always retried with the next tick, and a skipped send counts as neither a push nor a failure in `status`. Changes to a cookie's last-access time alone do not count as a change; the heartbeat carries them. `--verbose` logs each skipped send. Reported by @Dannyxlm in #149.
- A Linux sink with `live_cdp` on now re-injects the cookies from its last `/sync` into the running Chrome every 30 seconds, locally. Browser contexts an agent opened since the last push, and a Chrome that restarted, get cookies within 30 seconds, as they did when the source re-sent every tick. The cookies are held in memory only and pass the sink's current cookie policy on each tick. A failing re-inject is logged once, and again once when it recovers. Pull-mode sinks are unchanged. Reported by @Dannyxlm in #149.

### Live browser injection keeps your logins

- Chrome holds at most about 3300 cookies and evicts the least recently used past that, counting an injected cookie as used when it is set. A large batch (4868 cookies in the live test) silently evicted the first ~1800 it set, which included GitHub's session, so the agent's real browser stayed logged out while the push reported success. Live CDP injection now pushes only the 3000 most recently used cookies, oldest first, so the ones you use are kept. The sink log says how many were pushed and how many stayed sidecar-only.

### Teammates: quiet withdrawal of an answered notice; standing line first

- After a teammate pairs, the laptop withdraws its pairing notice. When the teammate had already answered it, the relay refuses the withdrawal with HTTP 409 and the daemon logged "could not withdraw notice ...: exit status 1". The tincan client now reports that refusal as `tincan.ErrNotCancellable`, and the lifecycle treats it as done (the notice is already cleared from the teammate's record) with no log line. Any other cancel failure is still logged.
- Docs: give each agent its standing line before adding it to `teammates.yaml`. An agent without the line may stop at its first pairing notice to ask the owner; a fresh code is sent automatically the next time it checks Tincan. `docs/teammates.md`, the README, and the steps `agentcookie teammates setup` prints now list the standing line first.
- Docs: a Linux teammate's browser must run with `--remote-debugging-port=9223 --remote-debugging-address=127.0.0.1` (the default `live_cdp.endpoint`) for live cookie injection. Without it cookies reach only the sidecar store for HTTP clients and the sink reports 0 contexts.

### Tailscale CLI works under launchd on the Mac app

- Every `tailscale` call (status, whois) now sets `TAILSCALE_BE_CLI=1`. The Tailscale.app binary acts as a CLI only when it sees a terminal, so under launchd it tried to start its GUI and failed. That left the teammate pairing notice waiting on "MagicDNS name unknown", refused teammate pulls because whois failed (fail closed), and made the push offline check fall back to dialing.

### Teammates: listed agents pull cookies from the laptop (groundwork)

- New owner file `~/.config/agentcookie/teammates.yaml` lists Tincan agent names (with an optional `cadence` of `turn` or `turn+timer`, plus optional `tincan_path` and `tincan_config`). Decoding is strict: unknown keys, duplicate names, and virtual Tincan agents (chatgpt, sesame, and `*-web` agents, which never have a Tailscale node) are rejected with a message saying why. See `examples/teammates.yaml`.
- `source --watch` reloads `teammates.yaml` when it changes, with no restart. If an edit does not parse, every teammate pull and pairing is paused until the file loads again, and the error is kept in `~/.agentcookie/teammates-state.json`.
- The daemon keeps teammate binding state (approved Tailscale node, pairing and pull history, `last_pulled_at`, `last_pull_error`) in `~/.agentcookie/teammates-state.json`, written atomically. It never writes `teammates.yaml` or `source.yaml`, and `source.yaml` gains no keys, so older binaries still load it.
- `source.yaml` may now have no push sink when `teammates.yaml` lists at least one teammate.
- A legacy push sink whose peer name is listed as a teammate is no longer dialed. Its key is kept, a one-time log line suggests deleting its entry from `source.yaml`, and push to it stays off even if the name is later removed from `teammates.yaml`.
- `GET /pull` authorizes per teammate: a teammate's key is served only while the name is listed and the request comes from the Tailscale node approved for it (checked with `tailscale whois`, cached per IP for 5 minutes; a WhoIs failure refuses). A request with a teammate's key from any other node is refused and logged as a security event. A removed teammate's key is refused and never falls back to the non-teammate rules. The legacy shared secret is refused on `/pull` while teammates are configured. Paired peers that are not teammates pull exactly as before.
- If `teammates-state.json` cannot be opened, every `/pull` with a paired key is refused (403), whether or not `teammates.yaml` exists: a teammate, or a leftover key of one whose list was deleted, cannot be told apart from another peer without it. Legacy pushes to named peers are held for the same reason. The same refusal applies while `teammates.yaml` exists but no good copy has loaded since the daemon started. A listed name's key is refused while teammates are suspended. A missing `teammates-state.json` opens empty, so configurations that never used teammates are unaffected.
- A keyless or failed-HMAC `/pull` from a paired teammate's approved node answers 401 and is reported internally as a lost key, for the automatic re-pair that a later release adds. When several paired teammates share that machine, the teammate named in the request's `X-Agentcookie-Teammate` header is the one re-paired; with no header nothing is re-paired and the laptop logs why.
- `teammates-state.json` (and the other daemon state files) are now fsynced before the rename and the directory after it, so a crash leaves either the old or the new file.
- The `/pull` listener no longer stays off for the life of the daemon when Tailscale is late at boot. It retries until a tailnet address exists, rebinds when that address changes, and records `pull_listener: up|down` in `source-state.json`.
- `POST /pair` is served on the same 9998 server while a teammate pairing is pending (404 otherwise). Codes are minted per teammate, single-use, valid for 30 minutes, accepted only from that teammate's approved node, and keep the existing attempt limit and 16 KB body cap (413 when exceeded). The key is stored as `keys/<teammate>.json`. The wire format is unchanged, so `pair --as sink` works against it. An oversized body on `pair --as source` now answers 413 instead of 400.
- Nothing changes for a configuration without `teammates.yaml`.

### Teammates: approval, pairing notices, and re-pairs on the laptop

- `source --watch` now drives each listed teammate on its own. It reads the Tincan roster (`tincan agents --json`, as the laptop's dedicated `agentcookie` Tincan agent with its own `TINCAN_CONFIG`) on start, every 10 minutes, and whenever `teammates.yaml` changes. `tincan` is run at an absolute path: `tincan_path`, else PATH, `~/bin`, then `~/.local/bin`. `tincan_config` defaults to `~/.config/tincan/agentcookie.json`.
- A listed agent with no machine on the roster waits, with no notice and no prompt. If the relay is older or this laptop is not on its `--admin` list, the roster shows no machines and every teammate stays waiting.
- The first time a teammate appears on a machine, or appears on a different machine, a dialog names the agent, the machine, its Tailscale login, the short node ID, and the machine it replaces. Approve binds exactly the node shown; if the roster moved to another node in the meantime nothing is bound and a new dialog shows the new one. Not now asks again only after a daemon restart or a machine change. Without a GUI session a plain notification and the log name the terminal command, `agentcookie teammates approve <name> --node <id>`.
- A teammate seen on a different machine stops syncing at once, its old machine's `keys/<name>.json` is deleted, and it needs that one approval. The same machine that lost its key (a keyless or failed `/pull` from its approved node) re-pairs with no prompt, at most once every 6 hours.
- After approval the teammate gets one Tincan notify: an install line pinned to this release's tag and the SHA256 of each Linux asset, `bash -s -- <tag> amd64=<sha256> arm64=<sha256>` (embedded in the laptop's release build at build time, so the laptop downloads nothing; a build that embeds only the amd64 digest is pinned for amd64 only), the `agentcookie join` command with a single-use 30-minute code and this laptop's MagicDNS name, its cadence (default by Tincan kind: `turn` for sandboxes, `turn+timer` otherwise), and the standing line to keep. An expired code is re-sent only after the roster shows the teammate called the relay since the last notice.
- A laptop build with no embedded release pin (a dev build, or a version that is not a release tag) sends no pairing notice. The teammate stays waiting and `teammates list`, `status`, and `doctor` say "this agentcookie build has no release pin; install a release build"; the daemon logs it once. Doctor gains a "Teammate release pin" line.
- At most one notice per teammate is open; an older one is withdrawn first. A notice held by the Tincan approval gate shows as held and is not resent; when the hold expires the code is re-sent once. A 429 or an unreachable relay backs off (1 to 30 minutes) and retries on a later poll, logging each new error once.
- A paired teammate that has not pulled for 6 hours while cookies changed gets a reminder to run `sink --pull-once`, at most once every 6 hours.
- Removing a name from `teammates.yaml` deletes `keys/<name>.json`, withdraws its open notice and pending code, and drops its machine binding. Cookies it already received stay valid on that machine. A key written by a pairing that finished during the removal is deleted on the next roster poll; keys of peers that were never teammates are never touched.
- A teammate whose `/pair` completed while its paired event was lost (a full event queue) is marked paired on the next roster poll.
- `tincan` and `tailscale whois` runs stop waiting for their output 2 seconds after a timeout, so a child process that keeps the output open cannot hang the daemon.
- The `agentcookie` agent's Tincan inbox is drained on every poll so relay notes do not pile up.
- New `agentcookie teammates` commands: `list` (state, machine, cadence, last pull; `--json`), `approve <name> --node <id>` (handed to the running daemon, which owns the state file), `remove <name>` (edits `teammates.yaml` keeping comments, and deletes the key), and `setup` (finds and records `tincan_path`, checks that the roster shows machines, and prints the one-time steps: `tincan invite agentcookie`, `tincan join` with its own `TINCAN_CONFIG`, the tailnet ACL rule to this laptop's port 9998 per teammate tag, and adding `agentcookie` to the approval gate's `unless` list).

### Release process: Linux digests embedded in the darwin build

- The laptop no longer downloads a release from GitHub to pin the teammate install line. A release build embeds the SHA256 of its published Linux tarballs via ldflags (`internal/cli.releaseLinuxAMD64SHA256`, `releaseLinuxARM64SHA256`).
- Release order is now: build the Linux tarballs, hash them with the new `scripts/linux-release-pins.sh <tag>`, then build the darwin binary with `make release-pinned TAG=<tag>` (which runs `make release LINUX_SHA256_AMD64=... LINUX_SHA256_ARM64=...` and checks the binary carries the digests). The release workflow's darwin job now waits for the Linux job and builds from its tarballs. See `docs/releasing.md`.

### Teammates: the teammate side (join and pull-once)

- New `agentcookie join --from <laptop-magicdns> --code <code> --as <name> [--proxy URL] [--cadence turn|turn+timer] [--replace]` sets up a teammate machine in one command. It writes a pull-mode `sink.yaml` pinned to that laptop (no listener), writes a sync-all `blocklist.yaml` when none exists (the laptop's blocklist decides what leaves it), pairs through the laptop's `/pair` on port 9998, stores the key under the laptop's name, and pulls once. Rerunning it is safe: an already-paired machine just pulls again, and a stored key the laptop no longer accepts is replaced using the new code. A different `--from` than the pinned laptop is refused without `--replace`.
- The proxy is `--proxy`, else the `proxy` saved in the agent's Tincan `client.json` (`TINCAN_CONFIG`, or Tincan's default path), else the one already in `sink.yaml`. It carries pairing and every pull, ahead of `HTTP_PROXY`/`HTTPS_PROXY`.
- `sink.yaml` gains an optional `pull:` block (`from`, `cadence`, `proxy`, `as`). `join` records `--as` as `pull.as`, and `--pull-once` names it in the `X-Agentcookie-Teammate` header of its `/pull` requests, so a laptop serving two agents on one machine re-pairs the right one after a lost key. With it, `listen.addr` is not required, and plain `agentcookie sink` polls the pinned laptop.
- New `agentcookie sink --pull-once` for each agent turn: one pull under `~/.agentcookie/pull.lock` (a second invocation exits 0 at once), a 45-second budget with connect retries, and the same apply path as continuous pull mode. Exit codes: 0 applied or nothing new, 3 laptop unreachable, 4 re-pair needed (no local key, or the laptop answered 401). With no local key it sends one keyless `/pull` to the pinned laptop, which starts the laptop's re-pair, and exits 4.
- For cadence `turn+timer`, `--pull-once` also starts one detached background pull loop if none is running (pidfile `~/.agentcookie/pull-loop.pid`, held locked for the loop's life, log in `~/.agentcookie/pull-loop.log`). The loop pulls every 5 to 15 minutes, jittered, and each pull takes `pull.lock`. A killed loop is restarted by the next turn.
- New `scripts/teammate-install.sh <tag> <arch>=<sha256>...` (for example `v1.4.0 amd64=<sha256> arm64=<sha256>`) installs the Linux release binary for the host's arch into `~/.local/bin`. It accepts only an exact release tag (not `latest` or a branch), checks the asset against the SHA256 the laptop's release build embeds for that arch (never a `checksums.txt` from the same release), refuses a host arch with no pin, and replaces the binary atomically.

### Teammates: doctor, status, and docs

- `agentcookie doctor` gains a teammates section when `teammates.yaml` exists: the pull listener (FAIL when down or not running), the `teammates.yaml` parse error (FAIL), a missing `tincan` or a roster without Tailscale nodes (WARN, fixed by `agentcookie teammates setup`), and one line per teammate with its state, machine, and last pull. A held notice, a machine waiting for approval, and a paired teammate with no pull for over 6 hours are WARN.
- Doctor blames the tailnet ACL only when a teammate polled the relay after its pairing notice and the code expired with no `/pair` attempt, and then prints the exact rule, for example `tag:grokbot -> your-mac.tail-xxxx.ts.net:9998`, using the node's Tailscale tag (or its node name when it has none). A code that expired before the teammate polled is reported as "has not picked up its pairing notice".
- The laptop records each teammate's last `/pair` attempt (`last_pair_attempt_at`, success or refusal) and keeps a picked-up but unanswered code as `pair_code_missed_at` across the automatic re-send, so the ACL diagnosis survives the next notice.
- `agentcookie status` (and `status --json`) shows a compact teammates block with the same diagnosis.
- New [docs/teammates.md](docs/teammates.md): setup, listing, the approval dialog, the standing line, Linux teammate details, rebuilds and lost keys, removal, and security notes.

### Push sinks back off instead of failing in a loop

- A push sink whose Tailscale peer is offline (a paused sandbox, a sleeping machine) is now skipped without being dialed. `source-state.json` marks it asleep and its failure count does not change. This also covers a sink URL that names a Tailscale IP directly (100.64.0.0/10 or fd7a:115c:a1e0::/48): the source checks the IP against `tailscale status` once per push and dials as before when status is unavailable or the IP is unknown.
- A sink that keeps failing is retried on an exponential backoff, starting at 30 seconds and capped at 30 minutes. The backoff resets on the next successful push and survives a source restart: `source --watch` and `source --once` pick up each sink's backoff from the previous run's `source-state.json`. Pushes inside the window skip that sink and still deliver to the others.
- Sinks are pushed concurrently, each under its own timeout, so a dead sink no longer delays or starves the healthy ones.
- Pushes started by the secrets-bus and discovery watchers share a 30-second rate cap, the same as the cookie watcher's, so a burst of file writes no longer starts a push per write.

### Keychain: --recreate keeps existing Chrome cookies readable

- `agentcookie wizard set-keychain-access --recreate` deleted Chrome Safe Storage and re-added it with a random value, so every existing Chrome cookie on that Mac became unreadable. The same step also ran after `--any-app`'s guard refused to touch an unreadable item, which bypassed that guard. The step now recreates an existing item with its own value, refuses to delete an item whose value it cannot read, and uses a fresh value only when no item exists yet.

### Installer re-runs upgrade in place

- Re-running `scripts/install-beta.sh` on a healthy, configured role no longer re-runs the wizard. It installs the new binary, restarts that role's LaunchAgent on it with `launchctl kickstart -k`, confirms it is running, prints `doctor`, and exits 0. A doctor FAIL, an unloaded daemon, a LaunchAgent that runs a different binary path, or a failed restart runs the wizard instead.
- The binary is written to a temp file beside the target and renamed into place, so the restarted daemon gets a fresh inode instead of a binary overwritten while running.
- `--code`, `--pair-url` and a `--peer` matching the configured peer no longer force the wizard, so the documented sink command can be re-run as is. `--skip-chrome-sqlite`, `--write-chrome-sqlite`, `--no-cdp`, an explicit `--skip-keychain-prompt` and a different `--peer` still do. The `--skip-keychain-prompt` the installer adds on its own without a TTY does not.
- New `--repair` flag forces a fresh pairing handshake. The healthy-path message names it.
- `--extra-binary` no longer breaks the install by passing an unknown flag to `wizard install`. The installer makes no Keychain change for it. It notes that CLIs signed by the same Developer ID team as agentcookie can already read Chrome Safe Storage, and points other CLIs at `agentcookie cookies --domain`. In v1.2.2 it instead printed a `wizard set-keychain-access --recreate` command; do not run that on a Mac whose Chrome cookies you need, because it replaces the Chrome Safe Storage key with a random value.
- Credits: @chronicgiardia wrote the original idempotency guard and the role-mismatch test (#132, #134).

### Signed binaries can quit Chrome through Apple Events

- Signed release and `make install` binaries now carry the `com.apple.security.automation.apple-events` entitlement. Hardened Runtime blocked chromectl's AppleScript quit without it, and the Google Chrome toggle under Privacy & Security > Automation could not be turned on. `scripts/sign.sh` fails with exit 3 unless the signed binary has the key set to true.
- chromectl bounds the AppleScript quit with its own short timeout. A not-authorized (-1743) result or an unanswered Automation prompt falls back to SIGTERM right away, and later quits in the same process skip AppleScript, so a sink sync never waits on a prompt nobody answers. Thanks to @CedricKhuaYanHan for the entitlement fix (#135).

### Sink: no Chrome Safe Storage reads under skip_chrome_sqlite

- A sink with `skip_chrome_sqlite: true` read Chrome Safe Storage on every batch to union extra Chrome profiles for adapters, so a headless sink showed a Keychain dialog per sync. Under `skip_chrome_sqlite`, nothing reads Chrome Safe Storage now: not the sink daemon, `agentcookie cookies --domain`, or doctor's Chrome stores check, which lists the stores as INFO without checking readability.
- With `skip_chrome_sqlite` off, the sink reads extra Chrome profiles only when at least one push adapter's CLI is installed. Installed adapters still receive the extra-profile cookies as in v0.15.
- A permanent Keychain refusal (no grant or timeout) is cached per browser for the life of the process and logged once; restart the sink to retry after granting access. A locked keychain is still retried on the next batch. Reported by @ericxyz86 (#137).

### Security: adapter cookie patterns bound to the real domain

- Sink adapters (airbnb, ebay, pagliacci, instacart, table-reservation) selected cookies with substring patterns such as `%ebay%`, which also matched look-alike hosts like `webay.com` or `ebay.attacker.com`. Those cookies were merged into the Cookie header sent to the real site (CWE-200, CWE-384). Adapters now match only the exact domain or its subdomains via `DomainHostPatterns`, and `doctor` uses the same matcher (#136).

### Universal 2 macOS releases

- `make release` builds one Universal 2 binary (arm64 + x86_64). The darwin archive is `agentcookie_<version>_darwin_universal.tar.gz`, so Intel Macs get the same signed, notarized binary as Apple Silicon.
- `scripts/install-beta.sh` walks releases newest-first and installs the newest stable asset this Mac can run (a universal build, otherwise the host architecture, including older hyphenated names). A linux-only latest release no longer aborts the macOS install. A GitHub API or download failure is reported as that failure, rather than as a missing macOS asset.
- The release workflow refuses to upload or publish a tag that has no universal darwin tarball.

### Pull / poll mode for client-only sinks

Sinks that can dial out over Tailscale but cannot accept inbound HTTP (Muse-like client-only shims) can poll the source instead of binding `/sync`.

- `agentcookie source --watch` serves `GET /pull` on the pairing port (Tailscale `100.x:9998` by default; `--pull-listen` overrides). The latest sealed envelope is HMAC-authenticated with the existing peer key.
- `agentcookie sink --pull-from <source-host> --pull-interval 30s` skips the sync listener and polls that endpoint. Fetched envelopes use the same decrypt → policy → sidecar/CDP path as POST `/sync`, and sequence tracking skips already-applied payloads.
- The shared HTTP client now sets `Transport.Proxy = http.ProxyFromEnvironment` so outbound polls honor `HTTP_PROXY` (required on sandboxes whose tailnet access is a local proxy).

### Dia source-browser support

Dia (The Browser Company) is a Chromium-family source adapter using the same Safe Storage model as Arc, Brave, and Edge. Set `browser.name: dia` in `source.yaml`. Discovery labels Dia's `User Data` root as `dia` so key lookup uses `Dia Safe Storage` rather than Chrome's.

### Multi-sink fan-out

One source can now push the same cookies and secrets to several sinks.

- `source.yaml` accepts a `sinks:` list, each entry with its own `url` and `peer`. A legacy single-sink config (`sink:` + `peer:`) keeps working unchanged and loads as a one-element list.
- A push reads and filters cookies once, then seals and POSTs to each sink with its own paired key. A missing per-sink key isolates that sink instead of aborting the whole push, and never silently downgrades a paired sink to the legacy shared secret.
- Per-sink failures are isolated; a partial or total sink failure is a non-zero `--once` exit. The `--once` deadline scales with sink count so a slow first sink cannot starve later healthy ones.
- `agentcookie wizard install --as source --add-sink --peer <host> --sink-url <url>` pairs and appends an additional sink to an existing config.
- `agentcookie status` and `agentcookie doctor` report per-sink push state.
- Example: `examples/source-multi-sink.yaml`.

### PP CLI config carriage (#118)

Auto-discovered Printing Press CLIs were pointed at the env-shaped `[secrets.file]` slot, so their `config.toml` went through a strict `KEY=VALUE` parser that cannot read the TOML they actually write. Every one of them failed to sync, and CLIs that were simply never authenticated were reported as errors on every push.

- A PP CLI's `config.toml` now rides as a `[[files]]` carriage item. Bytes are carried verbatim, so nested tables and comments survive and nothing is parsed.
- Carriage is gated on the manifest declaring at least one sensitive key, so a preference-only config is not swept in.
- A never-authenticated CLI is skipped quietly instead of erroring; a hand-written manifest naming a missing file still errors, because its author chose the path.
- `agentcookie secret link-configs` bridges carried configs into `~/.config/<cli>/config.toml`, where the installed fleet actually reads them (only binaries built after roughly 2026-07 honor an env pointer). Read-only planning, dry run until `--apply`. It never replaces an existing config, and the link is confined to `~/.config` so no symlink along the destination path can redirect it out of that tree.
- Measured across 59 installed CLIs: `secrets-bus` error lines 40 to 0, CLIs contributing secrets 2 to 9.

## [1.0.0] - 2026-08-13

### Featured: Mac to Linux continuous sync

The 1.0 release ships Mac to Linux cookie sync as the featured path. Your Mac's Chrome sessions flow to a Linux agent runtime (Grok Bot, cloud VM, homelab server) via live CDP injection over Tailscale.

**What's new:**

- Linux binaries: `agentcookie_1.0.0_linux_amd64.tar.gz` and `agentcookie_1.0.0_linux_arm64.tar.gz`
- Live CDP injection on Linux: cookies go into Chrome's in-memory store via `Storage.setCookies`
- No Chrome SQLite write on Linux (expected; success is the live CDP inject line)
- Tailscale-only transport with AES-256-GCM sealed envelopes

### Extra Chrome profile discovery (#115, #116)

- Mac extra Chrome profiles (Profile 1, Profile 2, etc.) are auto-discovered and decrypted
- Extra-profile cookies flow to the sidecar, adapters, and live CDP alongside Default profile cookies
- Sink adapters union extra-profile cookies through the same blocklist policy
- `agentcookie doctor` and `agentcookie status` report discovered profile stores
- Linux: discovery and doctor/status name stores, but Chrome SQLite is not decrypted (no libsecret); sidecar/plaintext and live CDP remain the Linux path

**Install:**

Download from [GitHub Releases](https://github.com/mvanhorn/agentcookie/releases/tag/v1.0.0). Verify against `checksums.txt`. See the README for the full how-to.

**Security defaults:**

- Linux sinks with missing `blocklist.yaml` or omitted `policy:` ship nothing (allowlist-empty). This is security-by-default.
- For a single-operator trusted box, write `blocklist.yaml` with `policy: blocklist` and `domains: []` to enable sync-all.
- The 1.0 release does NOT change this default in code. A later release may flip the default, which would be a breaking change.

**Honest limits:**

- Linux `wrote 0 cookies` is expected (success is live CDP inject)
- Google/DBSC cookies need local sign-in on the sink; copied cookies expire in minutes
- CDP port is loopback-only; same-user processes can attach to the debug port
- Cookie values never appear in logs

**Breaking changes from beta:**

- Archive naming changed to underscores: `agentcookie_1.0.0_darwin_arm64.tar.gz` (was `agentcookie-VERSION-darwin-arm64.tar.gz`)
- `install-beta.sh` pattern updated to match new archive names

## [Unreleased]

### Cookie allowlist policy

`blocklist.yaml` now accepts an explicit `policy` field. Omitted policy and
`policy: blocklist` preserve the existing behavior: missing or empty
`blocklist.yaml` means sync-all, and matching patterns are dropped.

Set `policy: allowlist` for high-trust/headless agent deployments where only
named cookie hosts should leave the source machine. Allowlist mode runs on both
source and sink; only matching `host_key` patterns pass, and an empty allowlist
syncs no cookie hosts. `agentcookie status` and `agentcookie doctor` report the
active policy, and `accounts on/off` remain blocklist-only helpers.

### cmux local loop on by default

When cmux is installed, `agentcookie wizard install` now wires the local
loop automatically: it sets cmux's `automation.socketControlMode` to
`allowAll` (backing up `~/.config/cmux/cmux.json` first), installs a
`dev.agentcookie.cmux-sync` LaunchAgent running `cmux-sync --watch` over
the full cookie set, and prints the one-time "restart cmux" step (the mode
is read only at app launch). After that single restart, this Mac's Chrome
session stays mirrored into cmux's browser hands-free.

New `agentcookie cmux-sync enable` / `disable` manage it directly;
`wizard install --no-cmux` opts out; `agentcookie doctor` reports loop
liveness (not-enabled / enabled-but-needs-restart / live). Absent cmux,
all of this is a clean no-op. Manual `cmux-sync --once/--watch` run inside
cmux still works with no socket change (a cmux child passes `cmuxOnly`).

### cmux local loop: `agentcookie cmux-sync`

Same-machine loop so this Mac's Chrome logins flow into this Mac's cmux
browser, with no sink, no peer, and no Tailscale hop. `cmux-sync --once`
does one read+inject cycle; `--watch` re-injects on every Chrome cookie
change (fsnotify, the same watcher `source --watch` uses). It reuses
`source.yaml`'s Chrome path and blocklist plus the shared decrypt + DBSC
read pipeline, and the cmux injection adapter from the sink surface.

Configure under a `cmux:` block in `source.yaml` (same shape as the sink
block); `--domain`, `--cmux-path`, `--browser` flags override. Run it from
inside cmux and it passes the default `cmuxOnly` socket gate with no cmux
change; the launchd path needs the socketControlMode change, which
`agentcookie doctor` now reports for the source-side loop as well as the
sink surface. Run the installed signed binary so reading Chrome's Safe
Storage key does not prompt (the grant is per-binary; `go run` prompts).

### cmux cookie-delivery surface (opt-in)

A fourth sink delivery surface that injects the synced session into
cmux's embedded WebKit browser after every sync via
`cmux rpc browser.cookies.set`, so an agent driving cmux's browser pane
wakes up authenticated. cmux holds its own WebKit cookie jar separate
from Chrome's SQLite, so this is purely additive.

Opt in with `cmux.enabled: true` in `sink.yaml` (optional `cmux_path`
and `domain_filter`). Implemented as a `sinkpush.Adapter`, so it inherits
blocklist/DBSC filtering, the non-fatal contract, and `wizard
verify-adapters` visibility, and is registered at sink startup only when
enabled. Cookie values pass through verbatim (no second App-Bound strip);
domains keep their leading dot (WebKit accepts it, unlike CDP); the
adapter opens and reuses one unfocused `about:blank` browser surface
since `browser.cookies.set` requires a surface and injected cookies
persist at the profile level.

`agentcookie doctor` gains a "cmux delivery" check that detects the
common gotcha: cmux's default `socketControlMode: cmuxOnly` rejects the
LaunchAgent sink, and the fix (set `allowAll`/`password` in
`~/.config/cmux/cmux.json` and fully restart cmux) is printed as the
remediation. See the "cmux delivery" section in the README.

### v0.14.0-beta.1: secrets bus adoption standard

A standard projects can adopt to opt into agentcookie sync. Layered on
the v0.13 wire format; no envelope changes.

Project authors drop a small `agentcookie.toml` manifest in their repo
(or installer). The agentcookie source machine walks well-known paths
on startup, discovers every manifest, reads the project's existing
.env file in place on every push, and ships the values to the sink
inside the existing v0.13 envelope. Zero manual `agentcookie secret
import-from` per project.

PP CLIs get adoption for free via an in-memory auto-detect adapter
that synthesizes a v2 manifest from each CLI's existing
`.printing-press.json` (using `auth_env_var_specs` to pick which keys
are secrets vs. config). The companion hand-off guide at
`docs/handoff-guides/for-printing-press-team.md` walks the
printing-press generator team through emitting explicit
`agentcookie.toml` per CLI + a bus-aware Go auth-load shim, so the
auto-detect becomes a fallback rather than the primary path.

last30days specifically: `docs/handoff-guides/for-last30days-team.md`
plus a drop-in manifest at `examples/adoption-last30days/`.

Three integration tiers, documented end-to-end:

- explicit-manifest: drop `agentcookie.toml`, auto-discovered
- pp-cli-derived: auto-detect from `.printing-press.json`
- legacy-v1: existing `~/.agentcookie/secrets/<cli>/` directories
  continue to work; the v1 imperative `secret import-from` path is
  unchanged

New commands:

- `agentcookie discover` - lists the registry, `--json` for scripting,
  `--verbose` for skipped manifests and reasons
- `agentcookie secret revoke <name>` - tier-aware removal

New artifacts:

- `docs/spec-agentcookie-secrets-bus-v2-adoption.md` - format spec
- `docs/handoff-guides/for-printing-press-team.md` - PP integration plan
- `docs/handoff-guides/for-last30days-team.md` - skill adoption plan
- `docs/runbook-adoption-manifest-author.md` - generic author runbook
- `examples/adoption-last30days/` + `examples/adoption-third-party-cli/`
- `pkg/agentcookieadoption/` - author-side Go helper for emitting + validating manifests

Out of scope for v0.14:
- printing-press generator integration (handled by external team per the hand-off guide)
- last30days repo changes (handled by external team per the hand-off guide)
- Python reader at `clients/python/agentcookie_secret` (still queued for v0.13.1)
- Signature verification (`signed_by` field reserved; v2.1)
- `[secrets.command]` and `[secrets.keychain]` source kinds (reserved; v2.1)

### v0.13.0-beta.1: secrets bus

A standardized, runtime-agnostic format for CLIs to consume auth tokens
that ride alongside cookies. New per-CLI directory at
`~/.agentcookie/secrets/<cli>/secrets.env` carries `KEY=VALUE` lines,
optional sealed twin at `secrets.env.sealed` under the v0.12 master key,
and an optional `manifest.toml` for per-key sync overrides.

Source watches the bus via fsnotify, includes the payload in the
existing push envelope, and the sink writes per-CLI files at mode 0600
after defensive name validation. Bus values win over env vars at the
reader-library level so users who set both expect the bus to override.

New `agentcookie secret` subcommand (list/get/set/rm/import-from/env)
gives the friend a one-shot path to seed the bus from an existing
config file. The doctor check (now 11 categories) reports cli count,
key count, and sealed/plaintext/mixed mode.

Companion artifacts:

- `docs/spec-agentcookie-secrets-bus-v1.md` -- format specification
- `docs/audits/2026-05-22-pp-cli-auth-inventory.md` -- audit of all 34 PP CLIs
- `docs/runbook-secrets-bus-adoption.md` -- migration runbook for CLI authors
- `docs/runbook-secrets-bus-gh-example.md` -- worked example for GitHub CLI
- `examples/gh-shim/` -- 50-line bash shim proving non-PP CLI consumability
- `pkg/agentcookiesecret/` -- Go reader library for in-process consumers

Python reader (clients/python/agentcookie_secret) is queued for
v0.13.1 alongside three documented v1.1 spec gaps: multi-account
namespacing, per-file local-only markers, and "device-bound-but
-shippable" third classification.

### v0.12.0-beta.6: skip keychain strategy loop on headless installs

**Friction #19 fix (2026-05-21 dry-run).** On a headless wizard install
(no TTY, `skip_chrome_sqlite: true` resolved), the v0.10
set-keychain-access strategy loop still fired. It exists to broaden
Chrome Safe Storage access for kooky-using PP CLIs that read Chrome's
encrypted SQLite directly. In headless mode no PP CLI reads Chrome's
SQLite -- the sidecar + adapter delivery paths take over -- so the
loop's 60-second timeout and alarming WARNING block were friction
with no benefit. The loop now skips when the wizard resolves to
headless mode. Explicit `--skip-keychain-access` and explicit
`--write-chrome-sqlite` paths preserve the pre-beta.6 behavior. The
Chrome Safe Storage prompt step is gated on the same condition.

Side effect: `resolveSinkHeadlessMode()` now fires once at the top of
`wizardInstallSink` instead of only inside the "write fresh sink.yaml"
branch, so upgrade-in-place installs get the same gating as fresh ones.

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
the canonical PP CLI install paths (one repo per CLI:
`github.com/mvanhorn/instacart-pp-cli`,
`github.com/mvanhorn/airbnb-vrbo-pp-cli`) plus a link to the
printing-press meta tool for the remaining three (eBay, Pagliacci,
table-reservation-goat). quickstart-beta.md gains a new "Install at
least one PP CLI on the sink" section between the sink install and
the verify steps.

**Known limit:** CDP injection drop rate is 55% globally and remains
high on some sites (notably instacart, where 2 of 33 cookies land).
The PP CLI path (over SSH, using the v0.11 adapter session file or
the v0.8 sidecar) is unaffected and is the recommended primary use
case. The CDP-into-Chrome-on-sink affordance is best-effort in this
release; deeper investigation lands in v0.12.0-beta.5+.

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
