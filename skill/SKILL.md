---
name: agentcookie-install
description: Install agentcookie on a Mac source and a Linux or Mac sink so Chrome cookies sync continuously over Tailscale. Use when the user says "install agentcookie", "set up cookie sync", "share my Chrome sessions with my agent box", or "make my agent log in as me".
version: 1.0.0
---

# agentcookie install

You are helping the user install agentcookie on two machines that are both on the same Tailscale tailnet, then pair them, so that the sink's Chrome stays continuously in sync with the source's Chrome.

After install, the user does not touch agentcookie again. The source watches Chrome via fsnotify and pushes every cookie change to the sink within seconds. On Linux, the sink injects cookies via CDP into an already-running Chrome. On macOS, a LaunchAgent keeps the daemon running across reboots.

## Inputs you need

1. Which machine is the **source** (the Mac the user logs into Chrome on, usually their laptop).
2. Which machine is the **sink** (where AI agents act - a Linux VM like Grok Bot, or a second Mac).
3. Tailscale is up on both.
4. On Linux: Chrome is running with `--remote-debugging-port=9223` (or another port).

If any of these are missing, stop and ask.

## Flow: Mac source to Linux sink (featured path)

### Step 0: detect the lay of the land

Run on the current machine:

```bash
uname -s  # Darwin = macOS, Linux = Linux
tailscale status 2>&1 | head -20
```

From the Tailscale status output, identify which host is the Mac and which is the Linux box.

### Step 1: confirm source vs sink with the user

Use the platform's blocking question primitive. Phrase it concretely:

> I see you're on `<current-hostname>`. Looks like `<other-hostname>` (Tailscale IP `100.x.y.z`) is your other machine. Should I install agentcookie with `<current-hostname>` as the source (your logged-in Chrome) and `<other-hostname>` as the sink (where your agents run)?

Confirm before proceeding. If wrong, ask which is which.

### Step 2: install on the Mac source

Install the binary if missing:

```bash
# Download from GitHub Releases
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_darwin_arm64.tar.gz
tar -xzf agentcookie_1.0.0_darwin_arm64.tar.gz
sudo mv agentcookie /usr/local/bin/
```

Or build from source:

```bash
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@v1.0.0
```

Run the source wizard. It blocks until pairing completes:

```bash
agentcookie wizard install --as source --peer <linux-hostname> &
WIZARD_PID=$!
```

Run in the background because we need to poll the pairing info file:

```bash
# Wait up to 30 seconds for the pairing info to appear.
for i in {1..120}; do
  if [ -f ~/.agentcookie/pairing.json ]; then break; fi
  sleep 0.25
done
cat ~/.agentcookie/pairing.json
```

Extract `code` and `pair_url` from the JSON output. These are what the sink needs. The code expires in 10 minutes.

### Step 3: install on the Linux sink

Do NOT run `wizard install --as sink` on Linux. The wizard omits the policy file (which means allowlist-empty / ship nothing) and can write `cdp.enabled: true` (which launches a second Chrome that fights your existing Chrome).

Instead, write the config files directly:

```bash
# Install the binary
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_linux_amd64.tar.gz
tar -xzf agentcookie_1.0.0_linux_amd64.tar.gz
sudo mv agentcookie /usr/local/bin/

# Create config directory
mkdir -p ~/.config/agentcookie

# Get the Tailscale IP
TAILSCALE_IP=$(tailscale ip -4)

# Write sink.yaml
cat > ~/.config/agentcookie/sink.yaml << EOF
listen:
  addr: ${TAILSCALE_IP}:9999

peer:
  hostname: <mac-hostname>  # REPLACE with Mac's Tailscale hostname

live_cdp:
  enabled: true
  endpoint: http://127.0.0.1:9223

skip_chrome_sqlite: true
EOF

# Write blocklist.yaml for sync-all on a trusted box
cat > ~/.config/agentcookie/blocklist.yaml << 'EOF'
version: 1
policy: blocklist
domains: []
EOF

# Pair with the Mac source
agentcookie pair --as sink \
  --peer <mac-hostname> \
  --code <code-from-pairing.json> \
  --pair-url <pair_url-from-pairing.json>
```

### Step 4: attach to existing Chrome (or start one as fallback)

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
```

Starting a second Chrome when one is already running on the same port causes conflicts. Always probe first.

### Step 5: start the sink

```bash
agentcookie sink
```

For a persistent daemon, write a systemd user unit:

```bash
mkdir -p ~/.config/systemd/user/
cat > ~/.config/systemd/user/agentcookie-sink.service << 'EOF'
[Unit]
Description=agentcookie sink
After=network.target

[Service]
ExecStart=/usr/local/bin/agentcookie sink
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now agentcookie-sink.service
```

### Step 6: verify the install

```bash
# On Mac
agentcookie doctor
agentcookie status --json

# On Linux
agentcookie doctor
agentcookie status --json
```

On Linux, look for:
- `live_cdp: endpoint reachable` - must be OK
- `tailnet: bind address` - must be OK
- `LastWriteMode` containing `livecdp` in status output
- `live_cdp: injected N cookies into M context(s)` in sync output

Ignore expected FAILs on Linux: codesign, Chrome.app path, launchctl (these are macOS-specific).

The message `wrote 0 cookies` for Chrome SQLite is expected on Linux. Success is the live CDP inject line.

### Step 7: report to the user

In plain language. Example:

> Done. agentcookie is running on both `<source>` and `<sink>`. The source pushes cookies as soon as they change in Chrome. The sink injects them via CDP into Chrome at port 9223. browserUse, Puppeteer, Playwright, or any Chromium automation connecting to that port will see your logged-in session.

## Flow: Mac source to Mac sink

For Mac-to-Mac, the wizard works:

```bash
# On the second Mac
agentcookie wizard install --as sink \
  --peer <source-mac-hostname> \
  --code <pairing-code> \
  --pair-url http://<source-mac>:9998/pair
```

The macOS sink writes to Chrome's encrypted SQLite, the plaintext sidecar, and per-CLI adapter session files.

## What to do if something errors

**`agentcookie: command not found`**: The binary is not on `$PATH`. Either use the full path (`/usr/local/bin/agentcookie`) or add the bin directory to PATH.

**Sink pairing returns `connection refused`**: Tailscale ACLs may be blocking tailnet-internal traffic on port 9998. Check `tailscale status` shows the source as reachable. If the source is online but unreachable, the user has restrictive ACLs to relax.

**`live_cdp: endpoint reachable FAIL`**: Chrome is not running with `--remote-debugging-port`. Start Chrome with the debug port, or check that the port in sink.yaml matches the actual port.

**`agentcookie status` reports zero syncs**: The source watcher has not seen a Chrome write yet. Open a tab on the source's Chrome (any domain) and refresh. The sync should appear within 2 seconds.

**Sink cookie store stays empty and nothing listens on `/sync`**: If the sink can dial out over Tailscale but cannot accept inbound HTTP (client-only Tailscale shims), POST `/sync` will never land. On the source, `agentcookie source --watch` serves `GET /pull`. On the sink, run `agentcookie sink --pull-from <source-hostname> --pull-interval 30s` instead of the listener. Pairing (`POST /pair` from sink to source) is outbound and still works.

**Doctor shows `sync-all` but cookies don't land**: The policy label and actual behavior can diverge. Verify success with `live_cdp: injected N cookies into M context(s)` in the sync output, not the policy label. Also check `LastWriteMode` contains `livecdp`.

## Out of scope for this skill

- Changing the cookie policy or allowlist/blocklist rules (the user edits `~/.config/agentcookie/blocklist.yaml` directly)
- Rotating pairing keys (re-run wizard on both sides)
- One source to many sinks (not yet supported)
