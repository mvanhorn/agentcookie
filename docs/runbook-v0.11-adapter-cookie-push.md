# Runbook: v0.11 sinkpush adapter cookie push

agentcookie sink ships with five built-in adapters that, after each
cookie sync, write the relevant subset of decrypted cookies directly
into each PP CLI's local session cache. PP CLIs then run from local
data on every invocation -- they never touch Chrome Safe Storage or
trigger Keychain prompts.

The user experience: one install of agentcookie (one Always Allow
click for the sink LaunchAgent itself), and every adapter-covered PP
CLI on the Mac mini stays logged in headlessly forever.

## Built-in adapters

| Adapter | Cookie hosts | Session-cache target |
|---|---|---|
| instacart-pp-cli | %instacart% | `~/.config/instacart-pp-cli/session.json` via `instacart auth paste` |
| airbnb-pp-cli | %airbnb% | `~/.config/airbnb-pp-cli/config.toml` + `cookies.json` |
| ebay-pp-cli | %ebay% | `~/.config/ebay-pp-cli/config.toml` + `cookies.json` |
| pagliacci-pp-cli | %pagliacci% | `~/.config/pagliacci-pp-cli/config.toml` + `cookies.json` |
| table-reservation-goat-pp-cli | %opentable.com, %exploretock.com | `~/.config/table-reservation-goat-pp-cli/session.json` |

If the CLI binary is not installed, its adapter is skipped (logged as
`skipped: CLI not installed`). If the cookie filter matches zero
cookies, the adapter is skipped with `skipped: no matching cookies`.
Adapter failures are reported in sink stderr and stored in
`~/.agentcookie/sink-state.json` but never abort the cookie sync.

## Install + verify on a Mac mini sink

Prerequisite: agentcookie wizard install --as sink has already run
(v0.10 step that grants the sink LaunchAgent Keychain access). v0.11
adds no new install-time keychain work -- the existing sink LaunchAgent
context already has everything the adapters need.

### 1. Deploy the v0.11 binary

```
ssh matts-mac-mini 'cp /tmp/agentcookie-v0.11 /Users/mvanhorn/bin/agentcookie && \
  launchctl bootout gui/$(id -u)/dev.agentcookie.sink && \
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.agentcookie.sink.plist'
```

### 2. Trigger a sync from the source

```
agentcookie source --once
```

### 3. Inspect adapter results

```
ssh matts-mac-mini '/Users/mvanhorn/bin/agentcookie wizard verify-adapters'
```

Expected output:

```
ADAPTER                          STATUS  PUSHED  DETAIL
-------                          ------  ------  ------
instacart-pp-cli                 ok      7
airbnb-pp-cli                    ok      21
ebay-pp-cli                      ok      18
pagliacci-pp-cli                 skip            no matching cookies
table-reservation-goat-pp-cli    ok      40

last run: 4s ago
```

If a row shows `FAIL`, the DETAIL column carries the error message
the adapter returned. Common cases:

- `CLI not installed` (Skipped, not a failure): adapter target binary
  is absent at `~/go/bin/<name>`. Install the CLI with `go install`
  to enable.
- `no matching cookies` (Skipped, not a failure): the cookie filter
  matched zero cookies in the latest sync. Either the source machine
  is not logged in to that service, or its cookies were not in the
  sync set.
- Exit-status errors: the adapter's target CLI exited non-zero on
  `auth paste`. The DETAIL column carries the CLI's stderr. Most
  often this means the CLI's import format has changed since the
  adapter was authored; file an issue.

### 4. Verify a CLI actually works

The point of the adapter is that subsequent CLI invocations work
without prompts:

```
ssh matts-mac-mini '/Users/mvanhorn/go/bin/instacart-pp-cli doctor'
ssh matts-mac-mini '/Users/mvanhorn/go/bin/airbnb-pp-cli doctor'
```

Expected: `[ok] session: N cookies from chrome` (or the CLI's
equivalent OK signal) with no Keychain prompt appearing on the Mac
mini desktop during the run.

If a CLI's `doctor` reports auth failure even though
verify-adapters showed `ok`, the adapter's written session file may
not match what the CLI expects. Inspect:

```
ssh matts-mac-mini 'cat ~/.config/<cli>/config.toml'   # pycookiecheat-style
ssh matts-mac-mini 'cat ~/.config/<cli>/session.json'  # table-reservation-style
```

## Adding a custom adapter

The five built-ins ship in `internal/sinkpush/`. A new adapter is
~50 lines of Go: implement the `sinkpush.Adapter` interface
(Name, CLIBinary, IsInstalled, CookieHostPatterns, Push) and
register it from init().

Reuse `PycookiecheatStyleAdapter` if the target CLI uses the
config.toml + cookies.json convention. For one-off session formats,
the table-reservation adapter is the cleanest reference.

The user-facing config schema for runtime-registered adapters
(YAML in ~/.config/agentcookie/sink.yaml) is deferred to a v0.12
follow-up; until then, custom adapters go in the agentcookie source
tree and require a rebuild.

## What v0.11 does not solve

- CLIs whose session-file format is undocumented or version-unstable.
  These need either an `auth paste`-style import command in the CLI
  itself, or per-version adapter maintenance.
- CLIs that use chromedp / DevTools rather than reading Chrome cookies
  from disk. The adapter pattern only helps the file-read population.
- Non-PP CLIs (third-party) with no adapter: same caveats as the
  built-ins. Each needs an adapter authored.

## Architectural rationale

v0.10's keychain ACL work proved that macOS's modern SecItem API
does not durably honor `-A` or `-T` trust list entries for
ad-hoc-signed Go binaries. Each new PP CLI triggered a fresh Always
Allow prompt; each rebuild re-prompted. The "one type ever NUX" the
product requires was unreachable through Keychain manipulation alone.

v0.11 sidesteps Keychain entirely on the CLI side: the sink (which
already has stable Keychain access in its LaunchAgent context) does
the read once, then writes the decoded result into each CLI's own
local cache. PP CLIs never touch Keychain at all; their auth runs
from the local cache forever, with no clicks needed beyond the
initial sink install.
