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

## What about Chrome's device-bound cookies (DBSC)?

Chrome's Device Bound Session Credentials (DBSC) tie a session to one machine's secure hardware so a stolen cookie cannot be replayed elsewhere. For a site that has adopted DBSC, a copied cookie works on the sink only until its short-lived window (minutes) lapses.

As of August 2026, the one broad adopter is Google's own account and Workspace cookies. The vast majority of sites, and every Printing Press CLI agentcookie feeds, do not use DBSC and sync as before.

For Google sessions: sign the sink's Chrome into the same Google account once. It establishes its own device-bound session locally, no cookie copy required.

The secrets bus (bearer tokens, API keys, OAuth refresh tokens) is untouched by DBSC and replicates normally.

## Status

### Working today

- Mac to Linux continuous sync via Tailscale `/sync`
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
- Python reader library for the secrets bus
- Signature verification on adoption manifests

## Documentation

| Doc | Use |
|---|---|
| [Architecture](docs/architecture.md) | module layout, sync lifecycle, security boundaries |
| [Protocol v1](docs/protocol.md) | wire format spec for future client implementations |
| [Threat model](docs/threat-model.md) | what agentcookie does and does not protect against |
| [FAQ](docs/faq.md) | common questions |
| [Consumption](docs/consumption.md) | how tools read synced cookies and secrets on the sink |
| [agent-sync runbook](docs/runbook-agent-sync.md) | browserUse / agent-browser via live CDP injection |
| [Secrets bus v2 adoption spec](docs/spec-agentcookie-secrets-bus-v2-adoption.md) | `agentcookie.toml` manifest format |
| [Install skill](skill/SKILL.md) | agent-executable installer prompt |

## License

MIT.
