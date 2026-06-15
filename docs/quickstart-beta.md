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

Cookie policy note: the default `blocklist.yaml` remains opt-out and syncs
everything unless a host matches a listed pattern. For a stricter headless agent
deployment, edit `~/.config/agentcookie/blocklist.yaml` on both machines and set
`policy: allowlist`, then list only the exact hosts and `%.subdomain` patterns
the sink should receive. `agentcookie doctor` reports the active mode.

## Install the sink side (your second Mac)

Same flow, opposite role:

1. SSH or screen-share into your sink Mac.
2. Extract the same release tarball.
3. Run: `./install-beta.sh --as sink --peer <macbook> --code <pairing-code> --pair-url <pair-url>` (the source's wizard install printed the code + URL for you to copy here).
4. The script verifies the code signature, places the binary, runs `agentcookie wizard install --as sink ...`, and ends with `doctor`.

On a GUI install (you're at the sink's keyboard, or you opened Terminal locally), you'll see one Keychain prompt asking permission for `agentcookie` to access Chrome Safe Storage. Click **Always Allow**.

### Headless sink (SSH-only, no monitor on the second Mac)

If the second Mac is headless and you're installing over SSH with no one at the screen to click prompts, `install-beta.sh` auto-detects "no TTY" and switches to headless mode:

- `sink.yaml` is written with `skip_chrome_sqlite: true` and `cdp.enabled: true`.
- The sink daemon NEVER reads Chrome Safe Storage. No Keychain prompt fires. The install completes click-free.
- Synced cookies land in the plaintext sidecar at `~/.agentcookie/cookies-plain.db`, in each PP CLI's session file via the v0.11 adapter push, and (via CDP injection) in Chrome's own SQLite at `~/.agentcookie/chrome-profile/` — a profile dedicated to agentcookie. Chrome encrypts its own SQLite with its own Safe Storage key on first launch; agentcookie never touches that key.

What this means in practice:

- **PP CLIs over SSH work immediately.** No Keychain prompts to dismiss.
- **Launching Chrome on the sink** against the agentcookie-owned profile (`open -a "Google Chrome" --args --user-data-dir=$HOME/.agentcookie/chrome-profile`) shows the synced sites already logged in. The default Chrome profile (`~/Library/Application Support/Google/Chrome/Default`) is untouched and won't see synced cookies — agentcookie keeps your browsing profile separate.
- **Opt out of CDP injection** if you don't want Chrome touched at all on the sink: pass `--no-cdp` to `install-beta.sh`. Sidecar + adapter push remain the cookie-delivery paths.
- **Opt out of headless mode** if you do have a GUI session and want the legacy Chrome SQLite write: pass `--write-chrome-sqlite` to `install-beta.sh`.

## Install at least one PP CLI on the sink

`agentcookie` syncs cookies between machines. The PP CLIs are what use those cookies to actually do something for you (browse Instacart carts, search Airbnb, etc.). You install them separately, on the sink.

Two of the five built-in-adapter PP CLIs are direct `go install`-able today:

```
GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/instacart-pp-cli@latest
GOPRIVATE='github.com/mvanhorn/*' go install github.com/mvanhorn/airbnb-vrbo-pp-cli@latest
```

The remaining three (eBay, Pagliacci, table-reservation-goat) ship through the [printing-press meta tool](https://github.com/mvanhorn/printing-press-library) — follow that repo's README to install them, or skip them for now if Instacart and Airbnb cover your use case.

Once at least one PP CLI is installed, verify over SSH from your laptop:

```
ssh second-mac 'instacart-pp-cli carts'
```

You should see your actual carts. If you get `command not found`, the install went to a `$GOPATH/bin` that isn't on the sink's `$PATH` — `ssh second-mac 'ls ~/go/bin/instacart-pp-cli'` to confirm it's there, then either invoke with the full path or add `~/go/bin` to the sink's shell profile.

These PP CLIs read cookies via the v0.11 adapter session files the sink writes after each sync — no env var setup needed. For other PP CLIs without built-in adapters, set `AGENTCOOKIE_PLAIN_COOKIES=~/.agentcookie/cookies-plain.db` in their environment to use the v0.8 sidecar path.

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
- **Device-bound sessions (DBSC).** Some sites bind a login to your laptop's secure hardware via Chrome's Device Bound Session Credentials. Today that is mostly Google accounts. A replicated cookie works on the sink for only a few minutes before Chrome there cannot refresh it. For Google, sign the sink's Chrome into the same account once and it gets its own device-bound session locally, no copy needed. agentcookie flags DBSC-suspect cookies in `agentcookie doctor` and ships them with a warning by default; pass `--skip-dbsc-suspect` to drop them. Non-DBSC sites and the entire secrets bus are unaffected. See [threat-model.md](threat-model.md).
- **First-time prompts.** macOS Gatekeeper triggers two Keychain prompts on the very first install (Chrome Safe Storage access, and the Tailscale interface check). One-time only.

## Help

This is a closed beta. If something's confusing, weird, or broken, DM the person who invited you. They want to know — the more rough edges you find now, the smoother every later release gets.

When you ping for help, paste the output of:

```
agentcookie doctor --json
```

That gives them everything they need to diagnose without 10 back-and-forth questions.
