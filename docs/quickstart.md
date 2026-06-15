# agentcookie quickstart

Five-minute install on a laptop-and-Mac-mini pair, assuming Tailscale is already running on both.

## Prerequisites

- Tailscale installed and signed in on both machines (free tier is fine for personal use)
- Chrome stable channel installed on both
- Go 1.24+ on both, OR pre-built binaries from the GitHub releases page
- The Mac mini auto-logs-in to a user session on boot (System Settings -> Users -> Login Options)

## Step 1: Install the binary on both machines

```
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest
mkdir -p ~/.config/agentcookie
```

## Step 2: Drop the example configs

On the **source** (laptop):

```
cd $(mktemp -d) && git clone https://github.com/mvanhorn/agentcookie.git
cp agentcookie/examples/source.yaml ~/.config/agentcookie/source.yaml
cp agentcookie/examples/blocklist.yaml ~/.config/agentcookie/blocklist.yaml
```

Edit `source.yaml`:
- `sink.url`: the sink's tailnet URL, e.g. `http://my-mac-mini.tailnet.ts.net:9999/sync`
- `peer.hostname`: the sink's tailnet hostname

Edit `blocklist.yaml` or run `agentcookie accounts off <domain>` for sites you do not want to sync. Empty blocklist means sync everything. For a stricter agent-runtime setup, set `policy: allowlist` in `blocklist.yaml` and list only the exact hosts/subdomains you want to sync; all other cookie hosts are dropped on both source and sink.

On the **sink** (Mac mini):

```
cp agentcookie/examples/sink.yaml ~/.config/agentcookie/sink.yaml
cp agentcookie/examples/blocklist.yaml ~/.config/agentcookie/blocklist.yaml
```

Edit `sink.yaml`:
- `listen.addr`: the sink's tailnet IP + port, e.g. `100.x.y.z:9999`
- `peer.hostname`: the source's tailnet hostname
- `cdp.enabled: true` if you want cookies to land in a running Chrome immediately (recommended)

## Step 3: Pair

On the source:

```
agentcookie pair --as source
```

You'll see:

```
agentcookie pair (source side)
  pairing code: YILU-OIVK
  source hostname: my-laptop.tailnet.ts.net
  listening on: 0.0.0.0:9998

  Run this on the sink machine within 10m0s
    agentcookie pair --as sink --peer my-laptop.tailnet.ts.net \
      --pair-url http://0.0.0.0:9998/pair --code YILU-OIVK

  Waiting for sink...
```

On the sink:

```
agentcookie pair --as sink --peer my-laptop.tailnet.ts.net \
  --pair-url http://my-laptop.tailnet.ts.net:9998/pair \
  --code YILU-OIVK
```

Both sides print a paired confirmation with a matching fingerprint.

## Step 4: Run the sink

On the Mac mini, install the launchd plist for auto-restart:

```
cp agentcookie/examples/launchd-sink.plist ~/Library/LaunchAgents/dev.agentcookie.sink.plist
# Edit the plist: replace REPLACE_WITH_FULL_PATH_TO_AGENTCOOKIE and REPLACE_WITH_USERNAME
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.agentcookie.sink.plist
```

Or just run it interactively while testing:

```
agentcookie sink
```

If `cdp.enabled: true`, launch Chrome with remote debugging:

```
open -na "Google Chrome" --args --remote-debugging-port=9222
```

## Step 5: Sync from the source

```
agentcookie source --once --verbose
```

Expected output:

```
agentcookie source: %instacart.com -> 12 cookies
agentcookie source: %granola.so -> 3 cookies
agentcookie source: posted 15 cookies, sink replied: ok: wrote 15 cookies via cdp; dropped 0 blocklisted cookies
```

Check on the sink: open Chrome (or use the running one), visit a synced site, you should be logged in.

## Step 6: Make it continuous

For now, `agentcookie source --once` is a single shot. Wire it to your preferred trigger (cron, launchd, fswatch on Chrome's Cookies file). Long-lived fsnotify-driven watch mode is on the roadmap.

A reasonable cron:

```
*/5 * * * * /path/to/agentcookie source --once >> ~/.agentcookie/source-cron.log 2>&1
```

Five-minute resolution is fine for most sites; session tokens generally rotate on the order of hours.

## Step 7: Device-bound sessions (DBSC)

A few sites bind a login to the source Mac's secure hardware through Chrome's Device Bound Session Credentials, so a replicated cookie only works on the sink for a few minutes before Chrome there cannot refresh it. Today this is mostly Google accounts. Do not rely on copied cookies for those sessions: sign the sink's Chrome into the same Google account once and it establishes its own device-bound session locally. agentcookie flags DBSC-suspect cookies in `agentcookie doctor` and, by default, ships them with a warning; pass `agentcookie source --skip-dbsc-suspect` (or set `AGENTCOOKIE_SKIP_DBSC_SUSPECT=1`) to drop them instead. Non-DBSC sites and the secrets bus are unaffected. See [threat-model.md](threat-model.md) for the full treatment.
