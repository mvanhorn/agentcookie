# agentcookie

Peer-to-peer Chrome session replication for AI agents.

Your laptop is logged in to everything. Your AI agents run on a different machine (Mac mini, cloud VM, whatever) and aren't. That gap is `agentcookie`.

## What it actually does

You browse normally on your laptop. agentcookie continuously syncs your Chrome session to a sink machine where your agents live. Agents on that sink then run any CLI that needs to be logged in to a website, with zero auth ceremony.

```
# One-time install
$ agentcookie wizard install --as source ...     # on your laptop
$ agentcookie wizard install --as sink ...       # on the sink (Mac mini)

# Then from your laptop, anytime:
$ agentcookie source --once
agentcookie source: posted 8283 cookies, sink replied: ok

# And from anywhere (SSH, Hermes, scheduled agent, ...):
$ ssh mac-mini 'instacart-pp-cli carts'
  Costco                 slug=costco   cart=757109404 items=5
  Safeway                slug=safeway  cart=3190      items=1

$ ssh mac-mini 'ebay-pp-cli auctions "watch" --has-bids --ending-within 1h'
12 active auctions:
  $352   23 bids   1m   Apple Watch Ultra 2 49mm Titanium ...
  $115   26 bids   1m   Gucci 5500M Steel Quartz ...
  ...

$ ssh mac-mini 'table-reservation-goat-pp-cli goat "omakase" --location seattle'
{ "results": [ { "name": "Omakase Dinner Series", "network": "tock", ... } ] }
```

No `auth login`. No Keychain prompt. No paste-the-cookie-string ritual. The CLIs read pre-populated session caches that agentcookie wrote during the last sync.

## Why this is hard

Existing that skill tools (1Password, Bitwarden, browser extensions) are built for humans switching accounts between two laptops they both touch. They assume someone will click "Merge" or open Chrome periodically.

agentcookie is built for the opposite workflow: continuous, one-way, unattended replication from the machine you live in to the machine your AI agents act from. No browser required on the sink. No third-party data plane. Pairing-derived keys, allowlists on both sides, encrypted over the Tailscale tailnet's WireGuard channel.

The hard part isn't moving bytes. It's making the cookies usable on the sink without a human at the keyboard. macOS's Keychain protections, Chrome's App-Bound Encryption (Chrome 127+), and per-CLI auth conventions each fight you. agentcookie handles all three.

## How it works

```
laptop                                              sink (Mac mini)
======                                              ===============

Chrome cookies change
  |
  v
agentcookie source --watch  (fsnotify on Chrome's Cookies SQLite)
  |
  | filter to allowlisted domains, decrypt with Keychain key
  v
+----- HTTPS over Tailscale (AES-256-GCM + replay-defended) -----+
                                                                 |
                                                                 v
                                              agentcookie sink (LaunchAgent)
                                                |
                                                | re-encrypt for sink Keychain
                                                v
                                              writes Chrome's Cookies SQLite
                                              + plaintext sidecar
                                              + adapter fan-out:
                                                  instacart  -> session.json
                                                  airbnb     -> config.toml + cookies.json
                                                  ebay       -> config.toml + cookies.json
                                                  pagliacci  -> config.toml + cookies.json
                                                  table-reservation-goat -> session.json
                                                |
                                                v
                                              PP CLIs read their own session caches
                                              forever, no Keychain access, no prompts
```

Five built-in adapters cover the [Printing Press](https://github.com/mvanhorn/printing-press-library) PP CLIs Matt uses most: instacart, airbnb, ebay, pagliacci, table-reservation-goat (OpenTable + Tock). New adapters are ~50 lines of Go and a `Register()` call; the runbook walks through it.

## Install (five minutes)

Prereqs: Tailscale running on both machines, Chrome installed, Go 1.22+ on both (or pre-built binaries from a release).

```
# On both machines:
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest

# On the laptop (source):
agentcookie wizard install --as source --peer <sink-hostname>

# It prints a pairing code. On the sink (Mac mini), paste:
agentcookie wizard install --as sink --peer <laptop-hostname> \
  --code <pairing-code> --pair-url http://<laptop-hostname>:9998/pair
```

The sink wizard installs a LaunchAgent that runs the long-lived sink daemon, expands the Chrome Safe Storage Keychain ACL for headless reads (v0.10), and registers the five adapters that fire after every sync (v0.11). One-time setup includes a single Always-Allow click for the sink LaunchAgent and one macOS login password prompt to broaden the Keychain. After that, all sink-side cookie work is headless forever.

See [docs/quickstart.md](docs/quickstart.md) for the long-form walkthrough.

## Verify it's working

```
agentcookie status                          # both daemons' last-sync state
agentcookie wizard verify-adapters          # per-adapter results from the most recent sync
agentcookie wizard verify-adapters --json   # same, structured for SSH agents
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

Pre-release. macOS-only on both ends today: source-side cookie paths and decryption are Chrome-on-macOS specific, and the sink relies on macOS Keychain + LaunchAgent. Linux and Windows support is on the roadmap but not yet wired up.

Working today:

- Continuous laptop -> sink sync (fsnotify on Chrome's Cookies file, debounced, allowlist-filtered, encrypted)
- Sink writes Chrome's Cookies SQLite in plain-v10 format + plaintext sidecar at `~/.agentcookie/cookies-plain.db`
- Five built-in PP CLI adapters push directly into each CLI's session cache after every sync
- Sink-side blocklist (defense in depth), sink-side replay defense (monotonic sequence per source)
- One-time install ceremony covered by `agentcookie wizard install`
- 165 unit tests across 20 packages

Not yet:

- `agentcookie pair --rotate` for live key rotation
- One-to-many fan-out (one laptop, multiple sinks)
- Linux + Windows source and sink support
- Signed release binaries (until then, `go install` is the only path; binary cdhash churn affects only the install ceremony, not steady state)

## Documentation

| Doc | Use |
|---|---|
| [Quickstart](docs/quickstart.md) | five-minute install on a laptop + Mac mini pair |
| [Architecture](docs/architecture.md) | module layout, sync lifecycle, pairing lifecycle, security boundaries |
| [Protocol v1](docs/protocol.md) | wire format spec for future client implementations |
| [Threat model](docs/threat-model.md) | what agentcookie does and does not protect against |
| [FAQ](docs/faq.md) | common questions |
| [v0.10 keychain runbook](docs/runbook-v0.10-keychain-access.md) | how the sink's one-time Keychain ACL setup works |
| [v0.11 adapter runbook](docs/runbook-v0.11-adapter-cookie-push.md) | adapter mechanism, validation, and how to add your own |
| [Install skill](skill/SKILL.md) | Claude Code / gstack-style skill so an agent can drive the install |

## License

Apache 2.0.
