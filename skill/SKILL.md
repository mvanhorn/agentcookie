---
name: agentcookie-install
description: Install and pair agentcookie on a source-and-sink pair of macOS machines. Use when the user says "install agentcookie", "set up cookie sync", or "pair my laptop with my Mac mini for agents".
version: 0.1.0
---

# agentcookie install

You are helping the user install agentcookie on two machines and pair them. The source machine is where the user logs in interactively (typically a laptop). The sink machine is where AI agents run (typically a Mac mini, a cloud VM, or any always-on macOS box).

## What this skill does

1. Detects which machine you are running on (source or sink).
2. Installs the `agentcookie` binary from `go install` (or links a local checkout if you have one).
3. Lays down the launchd plist so the sink restarts itself across reboots.
4. Walks the user through the pairing handshake.
5. Generates a starter `allowlist.yaml` based on the user's installed Printing Press CLIs, then asks them to confirm.

The skill never registers a hosted account, never sends anything off-machine, and never reads the user's existing cookies without explicit confirmation.

## Step 0: Ask which side

Use `AskUserQuestion` (or whatever blocking input primitive the platform exposes) to ask:

```
Which machine are we setting up?

A) Source (the machine where I log in to sites in Chrome - usually my laptop)
B) Sink (the machine that runs my AI agents - usually a Mac mini or cloud VM)
```

Set `ROLE` accordingly. Source goes through steps 1A-4A. Sink goes through 1B-4B.

## Step 1A: Install on source

```bash
# Use go install if Go is on PATH; otherwise tell the user to install Go 1.24+ first.
if command -v go >/dev/null 2>&1; then
  go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest
else
  echo "agentcookie needs Go 1.24+. Install from https://go.dev/dl/ then re-run this skill."
  exit 1
fi
mkdir -p ~/.config/agentcookie
```

Confirm `agentcookie version` works.

## Step 2A: Drop the example configs

```bash
curl -fsSL https://raw.githubusercontent.com/mvanhorn/agentcookie/main/examples/source.yaml -o ~/.config/agentcookie/source.yaml
curl -fsSL https://raw.githubusercontent.com/mvanhorn/agentcookie/main/examples/allowlist.yaml -o ~/.config/agentcookie/allowlist.yaml
```

Open `source.yaml` and ask the user to fill in:
- `sink.url`: the sink machine's tailnet URL (e.g. `http://my-mac-mini.tailnet.ts.net:9999/sync`)
- `peer.hostname`: the sink's tailnet hostname (used as the filename under `~/.config/agentcookie/keys/`)

Tell the user to leave `security.shared_secret` commented out; pairing will populate the keystore instead.

## Step 3A: Pre-fill the allowlist

Look in `~/printing-press/library/` for PP CLI binaries. For each binary, suggest the matching domain pattern based on the CLI's metadata (see the README of each CLI). Common mappings:

| PP CLI | Suggested allowlist pattern |
|--------|----------------------------|
| `instacart` | `%instacart.com` |
| `granola` | `%granola.so` |
| `superhuman` | `%superhuman.com`, `%mail.google.com` |
| `hubspot` | `%hubspot.com`, `%app.hubspot.com` |
| `airbnb` | `%airbnb.com` |
| `linear` | `%linear.app` |

Append the user-confirmed list to `~/.config/agentcookie/allowlist.yaml` under `domains:`.

## Step 4A: Start the pairing handshake

```bash
agentcookie pair --as source
```

The command prints a code like `XYZW-ABCD` and the sink-side run command. Read both back to the user and tell them to run the sink-side command on the OTHER machine within 10 minutes.

Wait. When pairing completes the command returns with a confirmation line and the derived-key fingerprint. Done.

## Step 1B: Install on sink

Same as Step 1A: `go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest`, create `~/.config/agentcookie/`.

## Step 2B: Drop the example configs

```bash
curl -fsSL https://raw.githubusercontent.com/mvanhorn/agentcookie/main/examples/sink.yaml -o ~/.config/agentcookie/sink.yaml
curl -fsSL https://raw.githubusercontent.com/mvanhorn/agentcookie/main/examples/allowlist.yaml -o ~/.config/agentcookie/allowlist.yaml
```

Fill in:
- `listen.addr`: the sink's tailnet IP + port (NOT 0.0.0.0; use the tailnet address Tailscale assigned)
- `peer.hostname`: the source machine's tailnet hostname
- `cdp.enabled: true` if you want cookies to land in a running Chrome (recommended; launch Chrome with `--remote-debugging-port=9222`)

## Step 3B: Install the launchd plist for unattended operation

```bash
curl -fsSL https://raw.githubusercontent.com/mvanhorn/agentcookie/main/examples/launchd-sink.plist -o ~/Library/LaunchAgents/dev.agentcookie.sink.plist
# Edit the plist to point ProgramArguments at the actual agentcookie binary
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.agentcookie.sink.plist
launchctl print gui/$(id -u)/dev.agentcookie.sink | head -5
```

## Step 4B: Run the sink-side pairing

The user gives you the pairing code and source hostname from Step 4A. Run:

```bash
agentcookie pair --as sink \
  --peer <source-hostname> \
  --pair-url http://<source-hostname>:9998/pair \
  --code <code>
```

On success, the keystore on this machine now has the derived key. The launchd-managed sink picks it up on next signal; if it was already running with the legacy `security.shared_secret`, restart it:

```bash
launchctl kickstart -k gui/$(id -u)/dev.agentcookie.sink
```

## Step 5: Verify

Both sides:

```bash
agentcookie status --json | jq .
```

The source side should report `peer.hostname` set and a key file present. The sink side should report `peer.hostname`, an allowlist with the chosen patterns, and (if you enabled CDP) `cdp.enabled: true`.

Then trigger a sync from the source:

```bash
agentcookie source --once --verbose
```

You should see one cookie batch land on the sink. On the sink, look for the log line:

```
agentcookie sink: wrote N cookies via cdp (dropped M non-allowlisted)
```

If CDP is enabled and Chrome is running with remote debugging, the cookies are visible to running pages immediately. Otherwise they are in the SQLite store and become visible on the next Chrome launch.

## Troubleshooting

- **Keychain prompt does not appear**: macOS already trusts the binary's previous Keychain access. If `agentcookie` errors with "Keychain access denied", open Keychain Access and remove the existing trust entry, then re-run.
- **CDP probe fails**: `lsof -i :9222 | grep -i chrome` to confirm Chrome is debuggable. Launch Chrome with `open -na "Google Chrome" --args --remote-debugging-port=9222`.
- **Pairing times out**: the source listens for 10 minutes. Re-run `agentcookie pair --as source` to get a fresh code.
- **Sequence rejected on /sync**: the sink restarted but the source kept its in-memory sequence counter. Source emits a new sequence each `--once` call (`time.Now().Unix()`), so the next sync recovers.

## Notes

- The skill never registers a hosted account. All state lives on the user's machines plus the Tailscale-managed tailnet.
- Allowlist on the sink is independent of the source's allowlist. The sink owner has the final say on what state lands in their Chrome.
- The shared-secret fallback in `security.shared_secret` is for users mid-migration from earlier prototypes. After pairing, delete that field from both YAMLs.
