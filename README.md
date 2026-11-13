# agentcookie

Your agent runs on a Linux box (a Grok Bot VM, a cloud agent runtime, a homelab server) and needs to act as you on every site you're already logged into. agentcookie keeps that box's Chrome session in sync with your Mac's, continuously, encrypted over your Tailscale tailnet, with zero per-site auth ceremony.

Cookie-authenticated sites show the logged-in UI after live CDP inject. Google/Workspace sessions stay logged out unless a human signed in on the box (DBSC binds those sessions to device keys). browserUse, Puppeteer, Playwright, or any Chromium automation that connects to Chrome's debug port sees your non-DBSC sessions already there.

## What it looks like

You browse normally on your Mac. agentcookie watches Chrome's Cookies file and ships the diff to your Linux sink the moment anything changes. On the Linux box, an agent does its work:

```
$ ssh grok-bot 'python3 -c "
from browser_use import BrowserUse
with BrowserUse(cdp_url=\"http://127.0.0.1:9223\") as b:
    print(b.page.goto(\"https://github.com/settings/profile\").title())
"'
Profile settings

$ ssh grok-bot 'instacart-pp-cli carts'
  Costco                 slug=costco   cart=757109404 items=5
  Safeway                slug=safeway  cart=3190      items=1
```

No `auth login`. No paste-the-cookie ritual. The agent's session was already there when the request hit.

## What this fixes

Logging in twice. Once on your Mac, once again on the Linux box where your agent runs. Per site, forever.

Tools that ship cookies between machines today assume a human is going to click "merge" or unlock a vault or open the destination browser. They were built for switching accounts between two laptops the same person uses. They weren't built for "the agent on the Grok Bot VM needs my session in 30 seconds and there's nobody home."

agentcookie is the second pattern. One-way, continuous, unattended replication from the Mac you live in to the Linux box your agents act from. Pairing-derived per-peer keys, cookie policy filters on both sides, AES-256-GCM over the Tailscale tailnet's WireGuard channel. The hard parts (macOS Keychain protections, Chrome's App-Bound Encryption on the source, live CDP injection on the sink) are handled.

## How it works

```
Mac (source)                                       Linux (sink)
============                                       ============

Chrome cookies change
(fsnotify on Cookies)
  |
  v
agentcookie source --watch
  - read SQLite (RO)
  - decrypt w/ Keychain key
  - filter by cookie policy
  - wrap in envelope
  - seal w/ peer key
  |
  +-- HTTPS over Tailscale (AES-256-GCM) ---------->  agentcookie sink
                                                        - listen 100.x:9999/sync
                                                        - decrypt seal
                                                        - filter by policy
                                                        - CDP attach to Chrome
                                                        - Storage.setCookies per
                                                          browser context

No Keychain on Linux. No Chrome SQLite rewrite. Just live CDP injection.
```

The sink injects cookies directly into Chrome's in-memory store via the Chrome DevTools Protocol. Chrome on the Linux box must be started with `--remote-debugging-port=9223` (or another port you configure). The inject happens on every sync and on every new browser context, so an agent that launches a fresh tab inherits the session immediately.

## Install

### Download release binaries

From the [GitHub Releases](https://github.com/mvanhorn/agentcookie/releases/tag/v1.0.0) page, download the archive for your platform:

| Platform | Archive |
|----------|---------|
| macOS arm64 | `agentcookie_1.0.0_darwin_arm64.tar.gz` |
| Linux amd64 | `agentcookie_1.0.0_linux_amd64.tar.gz` |
| Linux arm64 | `agentcookie_1.0.0_linux_arm64.tar.gz` |

Verify against `checksums.txt`:

```bash
# On Mac
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_darwin_arm64.tar.gz
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing

tar -xzf agentcookie_1.0.0_darwin_arm64.tar.gz
sudo mv agentcookie /usr/local/bin/

# On Linux
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_linux_amd64.tar.gz
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/checksums.txt
sha256sum -c checksums.txt --ignore-missing

tar -xzf agentcookie_1.0.0_linux_amd64.tar.gz
sudo mv agentcookie /usr/local/bin/
```

Or build from source after the tag:

```bash
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@v1.0.0
```

### Prereqs

- Tailscale running on both machines
- Chrome installed on both machines
- On Linux: Chrome started with `--remote-debugging-port=9223`

### Mac source setup

```bash
# 1. Run the source wizard (interactive)
agentcookie wizard install --as source --peer <linux-tailscale-hostname>

# The wizard prints a pairing code and URL. Keep this terminal open.
# Example output:
#   Pairing code: ABCD-EFGH-IJKL
#   Pair URL: http://your-mac.tailnet:9998/pair
#   Waiting for sink to pair...
```

### Linux sink setup (featured: Grok Bot / trusted single-operator box)

Do NOT run `wizard install --as sink` on Linux. The wizard omits the policy file, which means allowlist-empty (ship nothing). Instead, write the YAML files directly:

```bash
# 2. Create the config directory
mkdir -p ~/.config/agentcookie

# 3. Write sink.yaml
cat > ~/.config/agentcookie/sink.yaml << 'EOF'
listen:
  # Use your current Tailscale IP. After Tailscale re-auth, if this IP
  # becomes stale, the sink auto-rebinds to the new 100.x address.
  addr: 100.x.y.z:9999

peer:
  hostname: your-mac.tailnet  # Mac's Tailscale hostname

live_cdp:
  enabled: true
  endpoint: http://127.0.0.1:9223  # Chrome's debug port

skip_chrome_sqlite: true
EOF

# 4. Write blocklist.yaml for sync-all on a trusted box
cat > ~/.config/agentcookie/blocklist.yaml << 'EOF'
version: 1
policy: blocklist
domains: []
EOF

# 5. Pair with the Mac source
agentcookie pair --as sink \
  --peer your-mac.tailnet \
  --code ABCD-EFGH-IJKL \
  --pair-url http://your-mac.tailnet:9998/pair
```

Replace:
- `100.x.y.z` with your current Tailscale IP (`tailscale ip -4`). If Tailscale re-auth gives the sink a new IP, the sink auto-rebinds to it on next start.
- `your-mac.tailnet` with your Mac's Tailscale hostname (`tailscale status` on either machine)
- The pairing code and URL with the values printed by the Mac source wizard

### Teammates: your Tincan agents pull from the laptop

Give each of your own Agent Tincan agents one standing line first, then list it by name in `~/.config/agentcookie/teammates.yaml` on the laptop and approve its machine once in a dialog. (An agent that gets its pairing notice before it has the line may stop to ask you; a fresh code is sent automatically the next time it checks Tincan.) The agent then pulls fresh cookies at the start of every turn (and every 5 to 15 minutes if it is always on) over Tailscale, never through the Tincan relay. Only the Tailscale node you approved can pair or pull. Start with `agentcookie teammates setup`; `agentcookie doctor` shows each teammate's state and names a missing tailnet ACL rule. The full guide is [docs/teammates.md](docs/teammates.md).

### Linux teammate agents (pull from the laptop)

An agent the laptop lists as a teammate does not use the setup above. It pulls from the laptop, so it needs no listener and no inbound access:

```bash
# Install the pinned release (the laptop's pairing notice carries the tag and one SHA256 per arch)
bash teammate-install.sh v1.2.3 amd64=<sha256> arm64=<sha256>

# One-time setup, with the values from the notice
~/.local/bin/agentcookie join --from your-mac.tailnet.ts.net --code ABCD-EFGH-IJKL --as grokbot

# At the start of every turn
~/.local/bin/agentcookie sink --pull-once
```

`join` writes a pull-mode `sink.yaml` (no `listen.addr`), a sync-all `blocklist.yaml` if none exists, pairs, and pulls once. The proxy comes from `--proxy` or the `proxy` saved in the agent's Tincan `client.json`. `--pull-once` exits 0 when cookies are current, 3 when the laptop is unreachable (retry before the first logged-in action), and 4 when the machine needs to re-pair. With `--cadence turn+timer` it also keeps one background loop pulling every 5 to 15 minutes. The tailnet ACL must let the agent's tag reach `<laptop>:9998`. For live injection into the browser the agent drives, start that browser with `--remote-debugging-port=9223 --remote-debugging-address=127.0.0.1` (the default `live_cdp.endpoint` is `http://127.0.0.1:9223`); without it cookies land only in the sidecar store for HTTP clients and the sink reports 0 contexts.

### Linux CDP source (existing browser; no SQLite access)

When the authenticated browser already runs on Linux, set `cdp_source` in its
**source** configuration. AgentCookie reads the live jar through CDP, applies
the existing `blocklist.yaml`, and sends the normal paired encrypted envelope.
It never opens, copies, or decrypts Chromium's SQLite database.

```yaml
# ~/.config/agentcookie/source.yaml
sink:
  url: http://your-sink.tailnet:9999/sync
peer:
  hostname: your-sink
cdp_source:
  enabled: true
  endpoint: http://127.0.0.1:9230
```

The endpoint must be a bare `http` origin using a **literal loopback IP**
(`127.0.0.1` or `::1`); hostnames such as `localhost` are rejected so a hosts
or DNS override cannot redirect browser-control access off-host. Tailnet, LAN,
public, credential-bearing, and path/query endpoints are rejected. CDP-source
configuration is exclusive: do not set `chrome.db_path` or `browser`.
`export`, `agent-sync`, and `cmux-sync` also read from the configured CDP
endpoint in this mode, without falling back to another profile; their watch
modes poll rather than watching a SQLite file. `source --once` reads once;
`source --watch` polls every 10 seconds because CDP does not provide a
cookie-change event. CDP-source mode carries cookies only: it deliberately does
not scrape Local Storage or IndexedDB from an on-disk profile as a fallback.

### Attach to the existing Chrome (or start one as fallback)

On Grok Bot and most agent runtimes, Chrome is already running with a debug port. Probe before starting a new one:

```bash
# Check if Chrome is already listening on common debug ports
for port in 9223 9222 9224 9228 9229; do
  if curl -s "http://127.0.0.1:${port}/json/version" >/dev/null 2>&1; then
    echo "Chrome found on port ${port}"
    # Update sink.yaml to use this port
    sed -i "s|endpoint: http://127.0.0.1:.*|endpoint: http://127.0.0.1:${port}|" \
      ~/.config/agentcookie/sink.yaml
    break
  fi
done
```

If no Chrome is listening, start one as a fallback:

```bash
# Only if no existing Chrome debug port was found
google-chrome --remote-debugging-port=9223 &

# Or headless
google-chrome --remote-debugging-port=9223 --headless=new &
```

Starting a second Chrome when one is already running on the same port causes conflicts (the KTD2 failure mode). Always probe first.

You can also use `agentcookie doctor` which probes ports 9222, 9223, 9224, 9228, 9229, and 9400 and reports which endpoint is reachable.

### Start the sink

```bash
agentcookie sink
```

For a persistent daemon, copy the systemd user unit printed by `agentcookie wizard install --as sink` on macOS (or write your own). Do not auto-install it; review and place it yourself:

```bash
mkdir -p ~/.config/systemd/user/
# Paste the unit content
systemctl --user daemon-reload
systemctl --user enable --now agentcookie-sink.service
```

### Verify

```bash
# On Mac
agentcookie doctor
agentcookie status --json

# On Linux
agentcookie doctor
agentcookie status --json
```

On Linux, `doctor` reports expected FAILs for macOS-specific checks (codesign, Chrome.app path, launchctl). Look for:

- `live_cdp: endpoint reachable` - must be OK
- `tailnet: bind address` - must be OK
- Status output with `LastWriteMode` containing `livecdp`
- `live_cdp: injected N cookies into M context(s)` in sink output

The message `wrote 0 cookies` for Chrome SQLite is expected on Linux. Success is the live CDP inject line.

## Cookie policy

### Linux defaults to allowlist-empty (ship nothing)

On Linux, a missing `blocklist.yaml` or omitted `policy:` field means the sink accepts no cookies. This is security-by-default for untrusted sinks.

For a single-operator trusted box (like your own Grok Bot VM), the featured setup writes:

```yaml
version: 1
policy: blocklist
domains: []
```

This syncs all cookies. The 1.0 release does NOT change this default in code. A later release may flip the default, which would be a breaking change.

### For multi-user or less-trusted sinks

Use allowlist mode to sync only specific domains:

```yaml
version: 1
policy: allowlist
domains:
  - pattern: "github.com"
  - pattern: "%.github.com"
  - pattern: "%.openai.com"
```

## macOS sink (second Mac / Mac mini)

macOS sinks are still supported. The wizard works:

```bash
# On the second Mac
agentcookie wizard install --as sink \
  --peer <source-mac-hostname> \
  --code <pairing-code> \
  --pair-url http://<source-mac>:9998/pair
```

The macOS sink writes to Chrome's encrypted SQLite, the plaintext sidecar, and per-CLI adapter session files. It can also run CDP injection into a managed Chrome subprocess. See [docs/quickstart.md](docs/quickstart.md) for the full macOS-to-macOS walkthrough.

## macOS Login Items

macOS lists agentcookie's LaunchAgents in System Settings > General > Login Items. A release build installs a small signed app, `~/Applications/agentcookie.app`, and starts the source and cmux-sync jobs through it, so they show as "agentcookie" with its icon instead of under the signing certificate's personal name. The app holds only a launcher: it checks that the job's binary is a release-signed agentcookie, then runs it. agentcookie keeps its own Chrome folder and Keychain access.

The wizard and the installer set this up. To apply it to jobs installed by an older release, run once:

```bash
agentcookie services refresh
```

- `agentcookie services refresh --revert` runs every job directly again, without the app, and keeps it that way. `--wrap` undoes that.
- The sink always runs agentcookie directly, because it quits Chrome through an Automation grant that must stay agentcookie's own, so it still shows the signer's name.
- Don't delete `~/Applications/agentcookie.app` while jobs start through it. Run `services refresh --revert` first, or uninstall with `agentcookie wizard uninstall`, which removes the app with the last job.
- Going back to a release older than this one? Run `agentcookie services refresh --revert` with the current binary first. Older installers do not know the launcher and would treat a wrapped job as broken and rerun the wizard.
- `agentcookie doctor` has a Login Items line that says which jobs show as agentcookie and what to run if one doesn't.
- macOS may keep showing the old name until it re-registers the jobs, at the latest after your next login. If it still shows the old name after that, the last resort is `sfltool resetbtm` and a restart. That resets the Login Items approvals of every app, not just agentcookie.

## Claude in Chrome on two Claude accounts

Claude in Chrome pairs only with the Claude account it is signed into. To use it from two Claude Code accounts, give each account its own Chrome profile with the extension signed in, and copy your logins across once with `agentcookie profile-sync --to "Profile 1"`.

Chrome unloads a profile when its last window closes, which disconnects Claude in Chrome for that account. `agentcookie profile-keepalive` keeps every such profile loaded inside the running Chrome, with no window and no focus change. The wizard turns it on when two or more profiles are signed into different Claude accounts. Otherwise it does nothing. Like the other background jobs, it runs through `agentcookie.app`, which needs the signed release build (`install-beta.sh`): macOS blocks an unsigned build run by launchd from reading Chrome's profile folder.

```bash
agentcookie profile-keepalive enable                      # turn on (no-op with one account)
agentcookie profile-keepalive pause --profile "Profile 1" # hold off 10 minutes, e.g. before profile-sync
agentcookie profile-keepalive disable                     # turn off and keep it off
```

While it is on, closing a profile's windows does not cut Claude in Chrome's access to that profile. Only `disable` does. Pausing only stops reloads; to unload a profile the keep-alive already loaded (before `profile-sync`, say), pause it, then open a window of that profile and close it, or quit Chrome.

## Fan out to multiple sinks

One source can push the same cookies and secrets to several sinks. A source push reads and filters your cookies once, then seals and POSTs to each sink independently with that sink's own paired key. A sink that is down or unreachable is isolated: it fails on its own while the other sinks still receive the payload.

List sinks under `sinks:` in `source.yaml`, each with its own `url` and `peer`:

```yaml
sinks:
  - url: http://mac-mini.tailnet.ts.net:9999/sync
    peer: mac-mini
  - url: http://grok-bot.tailnet.ts.net:9999/sync
    peer: grok-bot
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
```

A legacy single-sink `source.yaml` (top-level `sink:` + `peer:`) keeps working unchanged and behaves as a one-element list. To pair and append another sink without hand-editing:

```bash
agentcookie wizard install --as source --add-sink \
  --peer <new-sink-hostname> \
  --sink-url http://<new-sink>.tailnet.ts.net:9999/sync
```

`agentcookie status` and `agentcookie doctor` report each sink's last push and failures. `agentcookie sinks list` shows each configured sink with its key and last push. See [examples/source-multi-sink.yaml](examples/source-multi-sink.yaml).

To stop feeding a sink (retired, or one you no longer trust):

```bash
agentcookie sinks remove grok-bot   # by peer, or by URL for a sink without one
```

It takes the sink out of `source.yaml`, keeping your comments, and deletes its `keys/<peer>.json`. A running source reads each key on every push, so the sink gets nothing from its next push on, and `GET /pull` refuses that key at once. Restart the source to drop the sink from its push list and from `status`. The key stays when another sink, a teammate or this machine's `sink.yaml` still uses that key file, or another key file holds the same key. Then the removed sink can keep pulling with it, even after a restart, until the key is rotated and every legitimate user of it is re-paired. `security.shared_secret` stays unless you pass `--revoke-shared-secret`, because pull-only sinks not listed in `source.yaml` may use it; while it stays, a removed machine that has it can keep pulling with it until you rotate it. With the flag it is removed for every machine that uses it, and a running source still accepts it on `GET /pull` until you restart it. Removing the last `sinks:` entry also removes a legacy `sink:`/`peer:` it was shadowing, and that peer's key unless something else uses it. The only sink cannot be removed unless `teammates.yaml` lists a teammate; pair another sink first, or run `agentcookie wizard uninstall`. Cookies and secrets the sink already received stay valid there until they expire or you sign out of those sites.

**Trust note:** every sink receives the same full cookie and secret set, so a compromise of the least-trusted sink exposes everything. Only list sinks you trust with the whole payload, and remove one with `agentcookie sinks remove` as soon as you stop trusting it. The device-bound (DBSC) caveat below is per-sink and unchanged: each sink still needs its own Chrome signed into the same Google account.

## Client-only sinks (inbound blocked)

Some sandboxes can dial out over Tailscale but cannot accept inbound HTTP, so the usual POST to the sink's `/sync` never lands and the cookie store stays empty.

On the Mac, `source --watch` already serves pairing on port 9998. It now keeps a listener up during watch and adds `GET /pull` (HMAC-authenticated with the paired key) that returns the latest sealed envelope.

On the blocked box, skip the sync listener and poll:

```bash
# Pairing is outbound (sink -> source /pair) and already works.
agentcookie pair --as sink --peer <mac-hostname> \
  --pair-url http://<mac-hostname>:9998/pair --code <code>

agentcookie sink --pull-from <mac-hostname> --pull-interval 30s
```

`--pull-from` accepts a hostname, `host:port`, or a full URL. Bare hostnames default to `http://<host>:9998/pull`. The poll client honors `HTTP_PROXY` / `HTTPS_PROXY`, which is required when the sandbox only reaches the tailnet through a local proxy. Crypto, envelope format, and Chrome read/write paths are unchanged.

The client-only box does not need its own `sinks:` entry on the Mac. Pairing writes the peer key; `/pull` seals with that key. Keep the Mac's existing push sinks as they are.

## What about Chrome's device-bound cookies (DBSC)?

Chrome's Device Bound Session Credentials (DBSC) tie a session to one machine's secure hardware so a stolen cookie cannot be replayed elsewhere. For a site that has adopted DBSC, a copied cookie works on the sink only until its short-lived window (minutes) lapses.

As of August 2026, the one broad adopter is Google's own account and Workspace cookies. The vast majority of sites, and every Printing Press CLI agentcookie feeds, do not use DBSC and sync as before.

For Google sessions: sign the sink's Chrome into the same Google account once. It establishes its own device-bound session locally, no cookie copy required.

The secrets bus (bearer tokens, API keys, OAuth refresh tokens) is untouched by DBSC and replicates normally.

## Status

### Working today

- Mac to Linux continuous sync via Tailscale `/sync`
- Client-only sinks via `GET /pull` + `sink --pull-from` when inbound HTTP is blocked
- Mac to Mac continuous sync (second Mac, Mac mini)
- Live CDP injection on Linux (cookies go into Chrome's in-memory store)
- Three cookie delivery surfaces on macOS sink (Chrome SQLite, plaintext sidecar, per-CLI adapters)
- Extra Chrome profile discovery: Mac profiles (Profile 1, Profile 2, etc.) are auto-discovered and decrypted; extra-profile cookies flow to sidecar, adapters, and live CDP alongside Default profile cookies
- Sink adapters union extra-profile cookies through the same blocklist policy
- Per-CLI secrets bus for bearer tokens and API keys
- 520+ unit tests across 26 packages

### Honest limits

- Linux sink writes 0 cookies to Chrome SQLite (expected; success is live CDP inject)
- Omitted cookie policy on Linux ships nothing (explicit `policy: blocklist` required for sync-all)
- CDP port is loopback-only; same-user processes can attach and read injected cookies
- Sidecar at `~/.agentcookie/cookies-plain.db` is plaintext at rest (not a success metric; verify with live CDP)
- Google/DBSC cookies need local sign-in on the sink; copied cookies expire in minutes
- Linux extra-profile Chrome SQLite stays unread (no libsecret); discovery and doctor/status name stores, but decryption requires macOS Keychain
- No live key rotation yet; re-run wizard on both sides to rotate
- Cookie values never appear in logs; do not use `cookies --json` as a verify step

### Not yet

- One source to many sinks fan-out
- Signature verification on adoption manifests

## Documentation

| Doc | Use |
|---|---|
| [Architecture](docs/architecture.md) | module layout, sync lifecycle, security boundaries |
| [Protocol v1](docs/protocol.md) | wire format spec for future client implementations |
| [Threat model](docs/threat-model.md) | what agentcookie does and does not protect against |
| [FAQ](docs/faq.md) | common questions |
| [Teammates](docs/teammates.md) | Tincan agents that pull cookies from the laptop: setup, approval, rebuilds, removal |
| [Consumption](docs/consumption.md) | how tools read synced cookies and secrets on the sink |
| [agent-sync runbook](docs/runbook-agent-sync.md) | browserUse / agent-browser via live CDP injection |
| [Secrets bus v2 adoption spec](docs/spec-agentcookie-secrets-bus-v2-adoption.md) | `agentcookie.toml` manifest format |
| [Install skill](skill/SKILL.md) | agent-executable installer prompt |

## License

MIT.
