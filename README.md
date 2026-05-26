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

The same is true for browser-driving agents. Point them at the agentcookie-managed Chrome profile on the second Mac and your agent sees the same logged-in state you have on your laptop. Or skip Chrome entirely: read the plaintext cookies sidecar at `~/.agentcookie/cookies-plain.db` from any agent that knows cookies, and the per-CLI secrets the same agent persisted under `~/.agentcookie/secrets/<cli>/secrets.env`.

## What this fixes

Logging in twice. Once on your laptop, once again on the Mac your agent lives on. Per site, per CLI, per API key. Forever.

Tools that ship cookies between machines today assume a human is going to click "merge" or unlock a vault or open the destination browser. They were built for switching accounts between two laptops the same person uses. They weren't built for "the agent on the headless Mac mini needs my session in 30 seconds and there's nobody home."

agentcookie is the second pattern. One-way, continuous, unattended replication from the machine you live in to the machine your agents act from. Pairing-derived per-peer keys, allowlist + blocklist on both sides, AES-256-GCM over the Tailscale tailnet's WireGuard channel. The hard parts (macOS Keychain protections, Chrome's App-Bound Encryption, per-CLI auth conventions) are handled.

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
                                                | one of three cookie delivery surfaces:
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

                                              plus the secrets bus mirror:
                                                ~/.agentcookie/secrets/<cli>/secrets.env  (mode 0600,
                                                  optional sealed twin under the v0.12 master key)
```

Three cookie surfaces because different agents read cookies differently. A browser-driving agent uses surface 1 (or its own profile pointed at the sidecar). A CLI with a built-in adapter uses surface 3. A raw cookies consumer uses surface 2. The sink runs all three after every sync, so the agent picks what fits.

Bearer tokens, API keys, and other per-CLI auth blobs ride the same encrypted push and land at `~/.agentcookie/secrets/<cli>/secrets.env` on the sink. CLIs read them via environment variables, the in-process `pkg/agentcookiesecret` Go library, or a project's own `agentcookie.toml` manifest (see the adoption standard below).

New cookie adapters are roughly 50 lines of Go and a `Register()` call; the runbook walks through it. New secrets bus consumers usually require no agentcookie-side change at all: drop an `agentcookie.toml` next to your CLI and `agentcookie discover` finds it.

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

macOS only on both ends today. The source side reads Chrome on macOS via the Keychain-backed decrypt path; the sink relies on macOS LaunchAgent and Keychain conventions.

Working:

- Continuous laptop to second-Mac sync via fsnotify on Chrome's Cookies file, debounced, allowlist + blocklist filtered, AES-256-GCM over Tailscale.
- Three cookie delivery surfaces on the sink (Chrome SQLite, plaintext sidecar, per-CLI adapter session files).
- Zero-config drop-in cookie adapters for five PP CLIs (instacart, airbnb, ebay, pagliacci, table-reservation-goat with OpenTable + Tock) on top of the universal surfaces; any other cookie-consuming agent reads the plaintext sidecar, any other secrets-consuming CLI reads the bus.
- Per-CLI secrets bus: bearer tokens, API keys, and `KEY=VALUE` auth blobs ride the same encrypted push and land at `~/.agentcookie/secrets/<cli>/secrets.env` (mode 0600) with an optional sealed twin.
- `agentcookie secret list / get / set / rm / revoke / import-from / env` for managing the bus, and `pkg/agentcookiesecret` as an in-process Go reader library.
- v2 adoption standard: drop an `agentcookie.toml` in your repo and `agentcookie discover` auto-detects it. Three integration tiers (explicit-manifest, pp-cli-derived auto-synthesized from `.printing-press.json`, and legacy v1 directories) coexist.
- Tailnet-only listeners on both ends; pair endpoint rate-limited with a 64-bit code.
- Persistent replay defense; per-peer pairing-derived keys.
- Apple Developer ID signed binaries; per-binary `-T` Keychain ACL on Chrome Safe Storage.
- Headless second-Mac install over SSH with no GUI clicks required.
- `agentcookie doctor` runs 11 health categories: binary signature, Tailscale, config, keystore, listener bind, sink/source state, sealing posture, adapter coverage, CDP injector health, and secrets bus coverage.
- 449+ unit tests across 26 packages.

Not yet:

- Python reader library at `clients/python/agentcookie_secret` (queued for v0.13.1; Go reader ships today).
- Signature verification on adoption manifests (`signed_by` field reserved; v2.1).
- `[secrets.command]` and `[secrets.keychain]` source kinds (reserved; v2.1).
- `agentcookie pair --rotate` for live key rotation. Today: re-run `wizard install` on both sides.
- One first-Mac, many second-Macs fan-out.
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
| [Secrets bus v1 spec](docs/spec-agentcookie-secrets-bus-v1.md) | wire format and on-disk layout for non-cookie auth |
| [Secrets bus v2 adoption spec](docs/spec-agentcookie-secrets-bus-v2-adoption.md) | `agentcookie.toml` manifest format and discovery rules |
| [Secrets bus adoption runbook](docs/runbook-secrets-bus-adoption.md) | migrating a CLI from imperative `secret import-from` to manifest-driven sync |
| [gh shim worked example](docs/runbook-secrets-bus-gh-example.md) | 50-line bash shim consuming the bus from a non-PP CLI |
| [Install skill](skill/SKILL.md) | Claude Code skill so an agent can drive the install |

## License

MIT.
