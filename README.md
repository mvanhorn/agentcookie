# agentcookie

Your agent runs on a Mac that isn't your daily driver. It needs to act as you on every site you're already logged into and against every API you've already authenticated. agentcookie keeps your second Mac's session state (Chrome cookies, per-CLI bearer tokens, API keys, and the auth blobs your tools persist next to them) in sync with your first Mac's, continuously, encrypted over your Tailscale tailnet, with zero per-site auth ceremony.

OpenClaw, Hermes, or any other agent runtime you point at the second Mac wakes up authenticated, on the web and in the terminal.

## What it looks like

You browse normally on your first Mac. agentcookie watches Chrome's Cookies file (and a parallel per-CLI secrets bus) and ships the diff to your second Mac the moment anything changes. On the second Mac, an agent does its work:

```
$ ssh second-mac 'instacart-pp-cli carts'
  Costco                 slug=costco   cart=757109404 items=5
  Safeway                slug=safeway  cart=3190      items=1

$ ssh second-mac 'ebay-pp-cli auctions "watch" --has-bids --ending-within 1h'
12 active auctions:
  $352   23 bids   1m   Apple Watch Ultra 2 49mm Titanium ...
  $115   26 bids   1m   Gucci 5500M Steel Quartz ...
  ...

$ ssh second-mac 'table-reservation-goat-pp-cli goat "omakase" --location seattle'
{ "results": [ { "name": "Omakase Dinner Series", "network": "tock", ... } ] }
```

No `auth login`. No Keychain prompt. No paste-the-cookie ritual. No re-entering API keys you already configured on your laptop. The agent's sessions were already there when the request hit.

The same is true for browser-driving agents and for any unmodified cookie tool. On a universal sink (the default), agentcookie writes your real Default Chrome profile and opens its Safe Storage key with a single login-password entry at install, so a tool that has never heard of agentcookie, yt-dlp, gallery-dl, a Polymarket CLI, a browser-driving agent, reads your synced, logged-in session with no per-tool setup. Prefer not to touch Chrome at all? Read the plaintext cookies sidecar at `~/.agentcookie/cookies-plain.db`, and the per-CLI secrets under `~/.agentcookie/secrets/<cli>/secrets.env`.

## What this fixes

Logging in twice. Once on your laptop, once again on the Mac your agent lives on. Per site, per CLI, per API key. Forever.

Tools that ship cookies between machines today assume a human is going to click "merge" or unlock a vault or open the destination browser. They were built for switching accounts between two laptops the same person uses. They weren't built for "the agent on the headless Mac mini needs my session in 30 seconds and there's nobody home."

agentcookie is the second pattern. One-way, continuous, unattended replication from the machine you live in to the machine your agents act from. Pairing-derived per-peer keys, blocklist filters on both sides, AES-256-GCM over the Tailscale tailnet's WireGuard channel. The hard parts (macOS Keychain protections, Chrome's App-Bound Encryption, per-CLI auth conventions) are handled.

## How it works

```
laptop                                              second Mac
======                                              ==========

Chrome cookies change      secrets bus change
(fsnotify on Cookies)      (fsnotify on ~/.agentcookie/
  |                         secrets/<cli>/secrets.env,
  |                         or autodiscovered via
  |                         agentcookie.toml manifests)
  |                                |
  +--------------+-----------------+
                 |
                 v
agentcookie source --watch  (decrypt Chrome with Keychain key,
                             filter against blocklist, fold in
                             secrets bus payload)
                 |
                 v
+----- HTTPS over Tailscale (AES-256-GCM, replay-defended) -----+
                                                                |
                                                                v
                                              agentcookie sink (LaunchAgent)
                                                |
                                                | cookie delivery surfaces:
                                                v
                                              1. Chrome's Cookies SQLite (re-encrypted for sink Keychain)
                                              2. Plaintext sidecar at ~/.agentcookie/cookies-plain.db
                                                 (env var: AGENTCOOKIE_PLAIN_COOKIES)
                                              3. Per-CLI adapter fan-out:
                                                   instacart  -> session.json
                                                   airbnb     -> config.toml + cookies.json
                                                   ebay       -> config.toml + cookies.json
                                                   pagliacci  -> config.toml + cookies.json
                                                   table-reservation-goat -> session.json
                                              4. cmux WebKit browser (opt-in) via
                                                   cmux rpc browser.cookies.set

                                              plus the secrets bus mirror:
                                                ~/.agentcookie/secrets/<cli>/secrets.env  (mode 0600,
                                                  optional sealed twin under the v0.12 master key)
```

Multiple cookie surfaces because different agents read cookies differently. Universal delivery (surface 1, the real Default profile plus the one-password Safe Storage open) is the default and is what makes any unmodified cookie tool work; the sidecar (surface 2) and per-CLI adapters (surface 3) are the agentcookie-aware paths that also work in degraded mode, when no login password is available to open the key. The sink runs surfaces 1 through 3 after every sync, so the agent picks what fits.

Surface 4 is the opt-in cmux surface. cmux ([cmux.com](https://cmux.com)) ships its own embedded browser on Apple WebKit with a cookie jar separate from Chrome's, so none of the other surfaces reach it. Enable it and the sink injects the synced session into cmux's browser after every sync (`cmux rpc browser.cookies.set`), so an agent driving cmux's browser pane wakes up authenticated. Injected cookies persist at cmux's profile level, so one injection carries to the agent's later panes. See [cmux delivery](#cmux-delivery-opt-in).

Bearer tokens, API keys, and other per-CLI auth blobs ride the same encrypted push and land at `~/.agentcookie/secrets/<cli>/secrets.env` on the sink. CLIs read them via environment variables, the in-process `pkg/agentcookiesecret` Go library, or a project's own `agentcookie.toml` manifest (see the adoption standard below).

New cookie adapters are roughly 50 lines of Go and a `Register()` call; the runbook walks through it. New secrets bus consumers usually require no agentcookie-side change at all: drop an `agentcookie.toml` next to your CLI and `agentcookie discover` finds it.

## cmux delivery (opt-in)

cmux's browser is Apple WebKit, with a cookie jar separate from Chrome's, so it needs its own surface. Enable it in `sink.yaml`:

```yaml
cmux:
  enabled: true
  # cmux_path: /custom/path/to/cmux   # optional; default resolves the app bundle, then PATH
  # domain_filter:                     # optional; SQLite-LIKE host_key patterns. empty = all synced cookies
  #   - "%github.com"
  #   - "%openai.com"
```

One required cmux-side step: cmux's RPC socket defaults to `socketControlMode: "cmuxOnly"`, which only accepts processes started inside cmux. The agentcookie sink is a LaunchAgent, not a cmux child, so with the default it is rejected and no cookies land. Open the socket to the sink:

```jsonc
// ~/.config/cmux/cmux.json
{
  "automation": {
    "socketControlMode": "allowAll"   // or "password" (then set automation.socketPassword)
  }
}
```

Then fully restart cmux (Quit and reopen). The mode is read only at app launch; `cmux reload-config` does not apply it. Verify with `cmux capabilities | grep access_mode` (it should no longer say `cmuxOnly`), or just run `agentcookie doctor`, which reports the cmux delivery surface and prints this exact remediation when the gate is still closed.

Caveats: the surface delivers cookies only, so sites whose session also lives in localStorage/IndexedDB or is device-bound (DBSC, e.g. Google/Workspace) may still need a one-time sign-in inside the cmux pane; WebKit's ITP can also drop some cross-site cookies. The surface is best-effort and non-fatal: if cmux is not running or still gated, the sync and the other three surfaces are unaffected.

### Local loop (one machine, no sink)

The sink surface above is for the two-machine model. If you just want *this* Mac's Chrome logins to flow into *this* Mac's cmux browser, use the local loop instead. No second machine, no Tailscale, no pairing.

```bash
# one-shot: read Chrome now, inject into cmux
agentcookie cmux-sync --once

# continuous: re-inject whenever Chrome cookies change (fsnotify)
agentcookie cmux-sync --watch

# narrow to specific sites
agentcookie cmux-sync --watch --domain "%github.com" --domain "%amazon%"
```

It reuses `source.yaml`'s Chrome path and blocklist (so your block rules still apply) and the same decrypt + DBSC filtering as `source`. Configure defaults under a `cmux:` block in `source.yaml` (same shape as the sink block above); flags override.

Run model and the `cmuxOnly` gate:

- **From inside cmux (recommended):** run `cmux-sync` in a cmux terminal. The process is a cmux child, so it passes the default `cmuxOnly` gate with no cmux change at all.
- **Unattended (launchd):** a LaunchAgent is not a cmux child, so it needs `socketControlMode` set to `allowAll`/`password` and a cmux restart (same as the sink, above). `agentcookie doctor` reports the local loop's state and prints the fix.

Keychain note: run the **installed, signed `agentcookie`** binary. Reading Chrome's Safe Storage key is a one-time Keychain grant for that signed binary (set up at `wizard install`), so it does not prompt. Running via `go run` or an unsigned/rebuilt binary will pop the macOS Keychain password prompt on every run, because the grant is scoped per binary.

## Install

Prereqs: Tailscale running on both Macs, Chrome installed, Go 1.22+ (or a pre-built release).

```
# On both machines:
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest

# On the first Mac (source):
agentcookie wizard install --as source --peer <second-mac-hostname>

# It prints a pairing code. On the second Mac (sink), paste:
agentcookie wizard install --as sink --peer <first-mac-hostname> \
  --code <pairing-code> --pair-url http://<first-mac-hostname>:9998/pair
```

The sink wizard installs a LaunchAgent, opens Chrome Safe Storage for universal delivery with one login-password entry over SSH (no GUI click), and registers the five built-in adapters that fire after every sync. If no password is available (a fully non-interactive install with no `AGENTCOOKIE_LOGIN_PASSWORD`), it lands in degraded mode (sidecar + adapters) and prints the one-line `agentcookie wizard set-keychain-access` upgrade command. After install, all sync work runs unattended.

See [docs/quickstart.md](docs/quickstart.md) for the long-form walkthrough and [docs/quickstart-beta.md](docs/quickstart-beta.md) for the headless flow if you're installing the second Mac over SSH.

## Verify it's working

```
agentcookie doctor                           # both sides' health
agentcookie wizard verify-adapters           # per-adapter results from the last sync
agentcookie wizard verify-adapters --json    # same, structured for SSH agents
```

Turn cookie sync off or back on for a site without editing YAML:

~~~bash
agentcookie accounts off x.com          # add x.com + subdomains to blocklist.yaml
agentcookie accounts on x.com           # remove those blocklist entries
agentcookie accounts list               # show disabled domains and custom patterns
~~~

Healthy output:

```
ADAPTER                          STATUS  PUSHED  DETAIL
-------                          ------  ------  ------
instacart-pp-cli                 ok      33
airbnb-pp-cli                    ok      25
ebay-pp-cli                      ok      51
pagliacci-pp-cli                 skip            no matching cookies
table-reservation-goat-pp-cli    ok      36

last run: 4s ago
```

## Status

macOS only on both ends today. The source side reads Chrome on macOS via the Keychain-backed decrypt path; the sink relies on macOS LaunchAgent and Keychain conventions.

Working:

- Continuous laptop to second-Mac sync via fsnotify on Chrome's Cookies file, debounced, blocklist filtered, AES-256-GCM over Tailscale.
- Three cookie delivery surfaces on the sink (Chrome SQLite, plaintext sidecar, per-CLI adapter session files).
- Works with Printing Press CLIs like Stripe, Linear, Notion, Granola, Slack, Kalshi, ElevenLabs, Mercury, and dozens more: anything with a bearer token or API key reads the secrets bus, anything that reads cookies reads the plaintext sidecar. Five PP CLIs (instacart, airbnb, ebay, pagliacci, table-reservation-goat with OpenTable + Tock) additionally get a bespoke zero-config cookie adapter.
- Per-CLI secrets bus: bearer tokens, API keys, and `KEY=VALUE` auth blobs ride the same encrypted push and land at `~/.agentcookie/secrets/<cli>/secrets.env` (mode 0600) with an optional sealed twin.
- `agentcookie secret list / get / set / rm / revoke / import-from / env` for managing the bus, and `pkg/agentcookiesecret` as an in-process Go reader library.
- v2 adoption standard: drop an `agentcookie.toml` in your repo and `agentcookie discover` auto-detects it. Three integration tiers (explicit-manifest, pp-cli-derived auto-synthesized from `.printing-press.json`, and legacy v1 directories) coexist.
- Tailnet-only listeners on both ends; pair endpoint rate-limited with a 64-bit code.
- Persistent replay defense; per-peer pairing-derived keys.
- Universal cookie delivery: one macOS login-password entry at install (no GUI click) opens the sink's Chrome Safe Storage key to any cookie reader via a partition list (`apple-tool:,apple:,teamid:<your-team>`), so unmodified cookie tools (yt-dlp, gallery-dl, browser-driving agents, the Printing Press CLIs) read the real synced Default Chrome profile. Verified live on macOS 15.x.
- Apple Developer ID signed binaries; the sink daemon reads Chrome Safe Storage via the `teamid:` partition (no per-binary trust list, no recreate of the key value).
- Headless second-Mac install over SSH: one login-password entry, no GUI SecurityAgent click. A box with no password available installs in degraded mode (sidecar + adapters still work) and prints the one-line upgrade command.
- `agentcookie doctor` runs fifteen health categories including cookie delivery (universal vs degraded, with duplicate-keychain-item race detection), binary signature + install, Tailscale, config, keystore, listener bind, sink/source state, sealing posture, adapter coverage, CDP injector health, secrets-bus + secret coverage, and DBSC-suspect cookies.
- 520+ unit tests across 26 packages.

Not yet:

- Python reader library at `clients/python/agentcookie_secret` (planned; the Go reader ships today).
- Signature verification on adoption manifests (`signed_by` field reserved; v2.1).
- `[secrets.command]` and `[secrets.keychain]` source kinds (reserved; v2.1).
- `agentcookie pair --rotate` for live key rotation. Today: re-run `wizard install` on both sides.
- One first-Mac, many second-Macs fan-out.
- At-rest sealing of the sidecar + adapter session files is wired in but off by default; turns on via `wizard set-keychain-access --enable-sealing` once consumer-side support lands.

## What about Chrome's device-bound cookies (DBSC)?

Chrome's Device Bound Session Credentials (DBSC) tie a session to one machine's secure hardware so a stolen cookie cannot be replayed elsewhere. That is exactly the "move a cookie to another machine" shape agentcookie is built on, so it is worth being precise about what DBSC does and does not change here.

DBSC is opt-in per site. A cookie becomes device-bound only when the site's own backend asks for it; nothing binds automatically, and a site binds only the specific session cookies it nominates. As of May 2026 the one broad adopter is Google's own account and Workspace cookies, and that protection went generally available on Chrome for Windows first. macOS support began rolling out gradually in the next Chrome release. The vast majority of sites agentcookie syncs, and every Printing Press CLI it feeds, do not use DBSC, so their cookies replicate to the second Mac and keep working exactly as before.

For a site that has adopted DBSC, a copied cookie works on the second Mac only until its short-lived window (minutes) lapses, because the second Mac cannot sign the refresh challenge that the source Mac's Secure Enclave holds. agentcookie does not try to defeat that. Instead the source flags cookies that look device-bound and, by default, ships them with a warning you can see in `agentcookie doctor`. Pass `--skip-dbsc-suspect` (or set `AGENTCOOKIE_SKIP_DBSC_SUSPECT=1`) to drop them instead of shipping cookies that will not survive on the sink.

Two things blunt the impact:

- The secrets bus is untouched. DBSC is a cookie protocol. Bearer tokens, API keys, and OAuth refresh tokens that ride the bus to `~/.agentcookie/secrets/<cli>/secrets.env` are outside its scope and replicate normally.
- For Google sessions specifically, copying cookies was never the right tool. Sign the second Mac's Chrome into the same Google account once and it establishes its own device-bound session there, no copy required. The agent on the sink reads that local session.

In short: DBSC narrows one corner of the web (today, mostly Google) and agentcookie is honest about it, while the bulk of what it syncs, non-DBSC site cookies and the entire secrets bus, is unaffected. See [docs/threat-model.md](docs/threat-model.md) for the full treatment.

## Documentation

| Doc | Use |
|---|---|
| [Quickstart](docs/quickstart.md) | install on a laptop + second-Mac pair |
| [Architecture](docs/architecture.md) | module layout, sync lifecycle, security boundaries |
| [Protocol v1](docs/protocol.md) | wire format spec for future client implementations |
| [Threat model](docs/threat-model.md) | what agentcookie does and does not protect against |
| [FAQ](docs/faq.md) | common questions |
| [Headless quickstart](docs/quickstart-beta.md) | SSH-only install on a headless second Mac |
| [v0.13 one-password keychain runbook](docs/runbook-v0.13-one-password-keychain.md) | universal delivery: the one-password Safe Storage partition open, the duplicate-item race + converge, and the unsigned-CGO boundary |
| [v0.10 keychain runbook](docs/runbook-v0.10-keychain-access.md) | legacy sink Keychain ACL setup (superseded by v0.13 for the grant path) |
| [v0.11 adapter runbook](docs/runbook-v0.11-adapter-cookie-push.md) | adapter mechanism + how to write your own |
| [v0.12 security runbook](docs/runbook-v0.12-security-hardening.md) | sealed master key, tailnet-only listeners, rate-limited pairing |
| [v0.12 codesign runbook](docs/runbook-v0.12-codesign.md) | Developer ID signing, notarization, CI secrets, renewal |
| [Secrets bus v1 spec](docs/spec-agentcookie-secrets-bus-v1.md) | wire format and on-disk layout for non-cookie auth |
| [Secrets bus v2 adoption spec](docs/spec-agentcookie-secrets-bus-v2-adoption.md) | `agentcookie.toml` manifest format and discovery rules |
| [Secrets bus adoption runbook](docs/runbook-secrets-bus-adoption.md) | migrating a CLI from imperative `secret import-from` to manifest-driven sync |
| [gh shim worked example](docs/runbook-secrets-bus-gh-example.md) | 50-line bash shim consuming the bus from a non-PP CLI |
| [Install skill](skill/SKILL.md) | Claude Code skill so an agent can drive the install |

## License

MIT.
