---
name: agentcookie-install
description: Install agentcookie on the user's source (laptop) and sink (Mac mini / cloud VM / second Mac) machines and pair them so Chrome cookies sync continuously over their Tailscale tailnet. Use when the user says "install agentcookie", "set up cookie sync", "share my Chrome sessions with my Mac mini", or "make my agent log in as me".
version: 0.2.0
---

# agentcookie install

You are helping the user install agentcookie on two machines that are both on the same Tailscale tailnet, then pair them, so that the sink's Chrome stays continuously in sync with the source's Chrome.

After install, the user does not touch agentcookie again. A LaunchAgent on each side keeps the daemons running across reboots. The source watches Chrome via fsnotify and pushes every cookie change to the sink within seconds. The sink writes via Chrome DevTools Protocol into a dedicated managed Chrome subprocess. No Keychain prompt fires on the sink. No screen-sharing required.

The user expects one prompt ("install agentcookie on my laptop and my Mac mini") to be enough. Make that real.

## Inputs you need

1. Which machine is the **source** (the machine the user logs into Chrome on, usually their laptop).
2. Which machine is the **sink** (the machine where AI agents act, usually a Mac mini or cloud VM).
3. Tailscale is up on both, and the user can SSH from source to sink without a password prompt.

If any of these are missing, stop and ask.

## Flow

### Step 0: detect the lay of the land

Run on the current machine:

```bash
which agentcookie 2>/dev/null || echo "missing"
/Applications/Tailscale.app/Contents/MacOS/Tailscale status 2>&1 | head -20
ssh -o ConnectTimeout=5 -o BatchMode=yes <suspected-sink> 'whoami' 2>&1
```

From the Tailscale status output, the current machine is the entry marked "active" or appears at the top. Every other macOS entry is a candidate sink.

### Step 1: confirm source vs sink with the user

Use the platform's blocking question primitive. Phrase it concretely:

> I see you're on `<current-hostname>`. Looks like `<other-hostname>` (Tailscale IP `100.x.y.z`) is your other Mac. Should I install agentcookie with `<current-hostname>` as the source (your logged-in Chrome) and `<other-hostname>` as the sink (where your agents run)?

Confirm before proceeding. If wrong, ask which is which.

### Step 2: install on the source

Install the binary if missing:

```bash
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest
```

Run the source-side wizard. It blocks until pairing completes:

```bash
agentcookie wizard install --as source --peer <sink-hostname> --local-name <source-hostname> &
WIZARD_PID=$!
```

Run in the background because we need to poll the pairing info file:

```bash
# Wait up to 10 seconds for the pairing info to appear.
for i in {1..40}; do
  if [ -f ~/.agentcookie/pairing.json ]; then break; fi
  sleep 0.25
done
cat ~/.agentcookie/pairing.json
```

Extract `code` and `pair_url` from the JSON output. These are what the sink needs.

### Step 3: install on the sink

SSH to the sink and run its wizard. You can pass everything on one line:

```bash
ssh <sink-hostname> "go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest && \
  agentcookie wizard install --as sink \
    --peer <source-hostname> \
    --code <code-from-pairing.json> \
    --pair-url <pair_url-from-pairing.json> \
    --local-name <sink-hostname>"
```

The sink wizard:
1. Drops `~/.config/agentcookie/{sink.yaml, allowlist.yaml}` with `cdp.managed: true` (zero Keychain involvement).
2. Runs the X25519 + HKDF pairing handshake against the source's pair URL.
3. Installs the sink LaunchAgent.
4. Starts the sink, which spawns a dedicated Chrome subprocess for the agent's cookie target.

### Step 4: confirm both daemons are up

```bash
launchctl list | grep dev.agentcookie
ssh <sink-hostname> 'launchctl list | grep dev.agentcookie'
```

Each should show `dev.agentcookie.source` (laptop) and `dev.agentcookie.sink` (Mac mini) with a PID.

### Step 5: verify a real sync round-trip

```bash
agentcookie status --json
ssh <sink-hostname> 'agentcookie status --json'
```

The source should report a recent push timestamp. The sink should report a recent write count. If either is empty, log into a github.com tab on the source's Chrome to force a cookie write and re-check.

### Step 6: report to the user

In plain language. Example:

> Done. agentcookie is running on both `<source>` and `<sink>`. The source pushes cookies as soon as they change in Chrome on `<source>`; the sink writes them into a dedicated Chrome instance at `~/.agentcookie/chrome-profile` on `<sink>`. Your agents on `<sink>` connect to that Chrome via CDP at `~/.agentcookie/chrome-profile`. After this install, the user does not run agentcookie commands by hand again.

## What to do if something errors

**`agentcookie: command not found` on the sink after `go install`.** The sink's `$PATH` lacks `~/go/bin`. Tell the user (or fix by sourcing `~/.zshrc` on the SSH command, or invoke the binary by absolute path: `~/go/bin/agentcookie`).

**Sink pairing returns `connection refused`.** Tailscale ACLs may be blocking tailnet-internal traffic on port 9998. Check `tailscale status` shows the source as reachable. If the source is online but unreachable, the user has restrictive ACLs to relax.

**Sink wizard hangs at `Chrome did not publish DevToolsActivePort`.** The managed Chrome subprocess failed to start. Most likely: Google Chrome is not installed at `/Applications/Google Chrome.app`. Install Chrome or set `cdp.chrome_binary` in sink.yaml.

**`agentcookie status` reports zero pushes after install.** The source watcher has not seen a Chrome write yet. Open a tab on the source's Chrome (any allowlisted domain) and refresh. Push should appear within 2 seconds.

**Sink Chrome subprocess crashes repeatedly.** Check `~/.agentcookie/logs/sink.err.log`. Most common cause: stale lockfile from a prior Chrome session sharing the user-data-dir. Solution: `rm ~/.agentcookie/chrome-profile/SingletonLock` and let the supervisor restart.

## Out of scope for this skill

- Code-signing the binary so Keychain access is granted without a prompt.
- Web Store extension install (planned for v0.3).
- Linux sink support (planned for v0.3).
- Bidirectional sync (planned for v0.3).
- Adding new domains to the allowlist after install (the user edits `~/.config/agentcookie/allowlist.yaml` on each side; LaunchAgents pick up changes on next restart, which they do automatically every 10 seconds after a config save).
