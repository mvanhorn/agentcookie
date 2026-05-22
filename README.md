# agentcookie

Your agent runs on a Mac that isn't your daily driver. It needs to act as you on every site you're already logged into. agentcookie keeps your second Mac's sessions in sync with your first Mac's, continuously, encrypted over your Tailscale tailnet, with zero per-site auth ceremony.

Whatever your agent uses to touch the web, OpenClaw, Hermes, Playwright, Puppeteer, chromedp, a CLI tool, raw HTTP, it wakes up authenticated.

## What it looks like

You browse normally on your first Mac. agentcookie watches Chrome's Cookies file and ships the diff to your second Mac the moment anything changes. On the second Mac, an agent does its work:

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

No `auth login`. No Keychain prompt. No paste-the-cookie ritual. The agent's sessions were already there when the request hit.

The same is true for browser-driving agents. Point a headless Chrome or a Playwright runtime at the agentcookie-managed profile on the second Mac and your agent sees the same logged-in state you have on your laptop. Or skip Chrome entirely: read the plaintext cookies sidecar at `~/.agentcookie/cookies-plain.db` from any agent that knows cookies.

## What this fixes

Logging in twice. Once on your laptop, once again on the Mac your agent lives on. Per site. Forever.

Tools that ship cookies between machines today assume a human is going to click "merge" or unlock a vault or open the destination browser. They were built for switching accounts between two laptops the same person uses. They weren't built for "the agent on the headless Mac mini needs my session in 30 seconds and there's nobody home."

agentcookie is the second pattern. One-way, continuous, unattended replication from the machine you live in to the machine your agents act from. Pairing-derived per-peer keys, allowlist + blocklist on both sides, AES-256-GCM over the Tailscale tailnet's WireGuard channel. The hard parts (macOS Keychain protections, Chrome's App-Bound Encryption, per-CLI auth conventions) are handled.

## How it works

```
laptop                                              second Mac
======                                              ==========

Chrome cookies change
  |
  v
agentcookie source --watch  (fsnotify on Chrome's Cookies SQLite)
  |
  | decrypt with Keychain key, filter against blocklist
  v
+----- HTTPS over Tailscale (AES-256-GCM, replay-defended) -----+
                                                                |
                                                                v
                                              agentcookie sink (LaunchAgent)
                                                |
                                                | one of three delivery surfaces:
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
```

Three surfaces because different agents read cookies differently. A browser-driving agent uses surface 1 (or its own profile pointed at the sidecar). A CLI with a built-in adapter uses surface 3. A raw cookies consumer uses surface 2. The sink runs all three after every sync, so the agent picks what fits.

New adapters are roughly 50 lines of Go and a `Register()` call; the runbook walks through it.

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

The sink wizard installs a LaunchAgent, configures Chrome Safe Storage access (or skips it cleanly on a headless install where there's no GUI session to click prompts), and registers the five built-in adapters that fire after every sync. After install, all sync work runs unattended.

See [docs/quickstart.md](docs/quickstart.md) for the long-form walkthrough and [docs/quickstart-beta.md](docs/quickstart-beta.md) for the headless flow if you're installing the second Mac over SSH.

## Verify it's working

```
agentcookie doctor                           # both sides' health
agentcookie wizard verify-adapters           # per-adapter results from the last sync
agentcookie wizard verify-adapters --json    # same, structured for SSH agents
```

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

macOS only on both ends today. The source side reads Chrome on macOS via the Keychain-backed decrypt path; the sink relies on macOS LaunchAgent and Keychain conventions. Linux and Windows are on the roadmap.

Working:

- Continuous laptop to second-Mac sync via fsnotify on Chrome's Cookies file, debounced, allowlist + blocklist filtered, AES-256-GCM over Tailscale.
- Three delivery surfaces on the sink (Chrome SQLite, plaintext sidecar, per-CLI adapter session files).
- Five built-in PP CLI adapters: instacart, airbnb, ebay, pagliacci, table-reservation-goat (OpenTable + Tock).
- Tailnet-only listeners on both ends; pair endpoint rate-limited with a 64-bit code.
- Persistent replay defense; per-peer pairing-derived keys.
- Apple Developer ID signed binaries; per-binary `-T` Keychain ACL on Chrome Safe Storage.
- Headless second-Mac install over SSH with no GUI clicks required.
- `agentcookie doctor` reports binary signature, Tailscale, config, keystore, listener bind, sink/source state, sealing posture, adapter coverage, CDP injector health.
- 330+ unit tests across 23 packages.

Not yet:

- More built-in adapters beyond the five above. Anything else uses the plaintext sidecar via `AGENTCOOKIE_PLAIN_COOKIES`.
- `agentcookie pair --rotate` for live key rotation. Today: re-run `wizard install` on both sides.
- One first-Mac, many second-Macs fan-out.
- Linux and Windows on either side.
- At-rest sealing of the sidecar + adapter session files is wired in but off by default; turns on via `wizard set-keychain-access --enable-sealing` once consumer-side support lands.

## Documentation

| Doc | Use |
|---|---|
| [Quickstart](docs/quickstart.md) | install on a laptop + second-Mac pair |
| [Architecture](docs/architecture.md) | module layout, sync lifecycle, security boundaries |
| [Protocol v1](docs/protocol.md) | wire format spec for future client implementations |
| [Threat model](docs/threat-model.md) | what agentcookie does and does not protect against |
| [FAQ](docs/faq.md) | common questions |
| [Headless quickstart](docs/quickstart-beta.md) | SSH-only install on a headless second Mac |
| [v0.10 keychain runbook](docs/runbook-v0.10-keychain-access.md) | sink's Keychain ACL setup |
| [v0.11 adapter runbook](docs/runbook-v0.11-adapter-cookie-push.md) | adapter mechanism + how to write your own |
| [v0.12 security runbook](docs/runbook-v0.12-security-hardening.md) | sealed master key, tailnet-only listeners, rate-limited pairing |
| [v0.12 codesign runbook](docs/runbook-v0.12-codesign.md) | Developer ID signing, notarization, CI secrets, renewal |
| [Install skill](skill/SKILL.md) | Claude Code skill so an agent can drive the install |

## License

Apache 2.0.
