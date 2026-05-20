# agentcookie closed-beta quickstart

Welcome. You're getting an early invite to agentcookie because someone trusts you to find rough edges and tell them about it. This guide takes you from "two Macs and a Tailscale tailnet" to "AI agents on the second Mac act as you on every site you're logged into" in ten minutes.

## What you're building

```
your MacBook (source)                          your second Mac (sink)
   - you browse Chrome here                       - agents run here over SSH
   - Chrome's logged-in sessions sync ----->      - cookies land here automatically
                                                  - any PP CLI you install works without login
```

After install, you'll be able to:

```
ssh second-mac 'instacart-pp-cli carts'
  Costco                 slug=costco   cart=757109404 items=5
  Safeway                slug=safeway  cart=3190      items=1
```

with no `auth login`, no Keychain prompt, no copy-paste-the-cookie ritual.

## Prereqs

- Two Macs running macOS 14 or later. Apple silicon recommended. One you browse on (we'll call it source); one your agents run on (sink). Many people use a Mac mini for the sink.
- Both Macs on the same Tailscale tailnet. Run `tailscale status` on each; both should appear in each other's list. If not, set up Tailscale first.
- Google Chrome installed on the source. Sign in to whatever sites you want your agents to act on.
- The release tarball (your invite includes a link or `gh release download` instructions).

Optional: Go 1.22+ if you want to build from source. Not required when using the release tarball.

## Install the source side (your MacBook)

1. Download `agentcookie-v0.12.0-beta.1-darwin-arm64.tar.gz` from the release link in your invite.
2. Extract: `tar -xzf agentcookie-v0.12.0-beta.1-darwin-arm64.tar.gz`. The bundle contains `agentcookie`, `install-beta.sh`, and this guide.
3. Run the install script: `./install-beta.sh --as source`. It will:
   - Verify your binary is notarized (so macOS doesn't block it)
   - Place it at `/usr/local/bin/agentcookie` (or `~/bin/agentcookie` if you don't have admin)
   - Prompt for the sink machine's Tailscale hostname (e.g. `second-mac`)
   - Run `agentcookie wizard install --as source --peer <sink>` interactively
   - End by printing a pairing code

Save the pairing code. You'll need it on the sink.

## Install the sink side (your second Mac)

Same flow, opposite role:

1. SSH or screen-share into your sink Mac.
2. Extract the same release tarball.
3. Run: `./install-beta.sh --as sink --peer <macbook> --code <pairing-code> --pair-url <pair-url>` (the source's wizard install printed the code + URL for you to copy here).
4. The script verifies the code signature, places the binary, runs `agentcookie wizard install --as sink ...`, and ends with `doctor`.

You'll see one Keychain prompt asking permission for `agentcookie` to access Chrome Safe Storage. Click **Always Allow**.

### Headless sink (SSH-only, no monitor on the second Mac)

If the second Mac is headless and you're installing over SSH with no one at the screen to click prompts, `install-beta.sh` auto-detects "no TTY" and adds `--skip-keychain-prompt` to the wizard. The install completes, but the sink daemon won't be able to read Chrome Safe Storage until you grant Always Allow once. To do that, either physically log into the sink Mac or open a Screen Sharing session, then run:

```
~/bin/agentcookie sink
```

That triggers the Keychain prompt in the GUI session. Click **Always Allow**, then Ctrl-C and restart the LaunchAgent: `launchctl bootout "gui/$(id -u)/dev.agentcookie.sink"; launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/dev.agentcookie.sink.plist`. Sync resumes within seconds.

## Verify both sides

On both Macs:

```
agentcookie doctor
```

Expect to see all green:

```
agentcookie doctor v0.12.0-beta.1
  [OK]   Binary signature: Developer ID Application (NM8VT393AR)
  [OK]   Tailscale: 100.x.y.z reachable
  [OK]   Config: source.yaml present, parses OK
  [OK]   Keystore: peer key for second-mac present
  [OK]   Source state: last push 4m ago, 0 failures
all green
```

If a line shows FAIL, follow the remediation it prints. Most common: Tailscale wasn't running when you started the daemon — `tailscale up`, then re-run doctor.

## First sync

On the source side, push one sync manually:

```
agentcookie source --once
```

Expect: `agentcookie source: posted N cookies, sink replied ok`.

After this completes, the source daemon takes over and pushes any time Chrome's cookies change. You don't need to run `--once` again unless you want to.

## Use it

On the sink, install any [Printing Press](https://github.com/mvanhorn/printing-press-library) PP CLI that needs an authenticated session:

```
go install github.com/mvanhorn/printing-press-library/library/instacart-pp-cli@latest
```

Then run it from anywhere — locally, over SSH, from an agent that has shell access:

```
ssh second-mac 'instacart-pp-cli carts'
```

That's it. No login flow inside the CLI. The cookies the source pushed are already in place.

## Known limits in the closed beta

- **Plaintext sidecar at rest.** v0.12 closed beta ships with cookie sealing OFF by default. A non-`agentcookie` process running on your sink Mac as your user can read every cookie value out of `~/.agentcookie/cookies-plain.db`. If your sink Mac runs untrusted code (it shouldn't for a personal agent host), this is your risk-accept. The sealing infrastructure is wired up; we'll flip it on by default in a future release once PP CLIs catch up.
- **macOS only.** Linux/Windows sinks are roadmap.
- **No live key rotation.** If you suspect a paired key is compromised, run `agentcookie wizard install` again on both sides to repair.
- **eBay sessions die fast.** eBay's server binds sessions to your laptop's device fingerprint; replicated cookies fail `ebay-pp-cli` auth checks within hours of your last laptop login. Other PP CLIs are fine.
- **First-time prompts.** macOS Gatekeeper triggers two Keychain prompts on the very first install (Chrome Safe Storage access, and the Tailscale interface check). One-time only.

## Help

This is a closed beta. If something's confusing, weird, or broken, DM the person who invited you. They want to know — the more rough edges you find now, the smoother every later release gets.

When you ping for help, paste the output of:

```
agentcookie doctor --json
```

That gives them everything they need to diagnose without 10 back-and-forth questions.
