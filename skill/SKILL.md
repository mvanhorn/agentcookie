---
name: agentcookie-install
description: Install explicit browser-source and live Chrome destination session sync over Tailscale, including experimental Zen and Firefox sources.
version: 2.0.0
---

# agentcookie install

## Required inputs

1. **Which browser do you log in with?** Never assume Chrome. Record browser and
   source machine, destination machine, selected profile, and intended accounts.
2. Confirm both machines and Tailscale peer identities. Do not pair an inferred
   third-party machine or inspect unrelated browser profiles.
3. Choose destination: new GUI Mac `live_chrome` (owned profile), Linux/non-GUI
   `attach` (explicit loopback CDP endpoint), or deliberate legacy `ppcli`.
4. Resolve container/site choices. Ambiguous accounts stay held unless the user
   explicitly accepts that. Holding is not successful multi-account delivery.

Experimental Gecko support is not a full-matrix release claim. Check the
candidate's [release evidence](../docs/release-gates.md). Never promise that every
session transfers: engine-bound sessions may need a fresh login after Gecko→Chrome.

## Source flow

Install a verified release or build the reviewed candidate; do not use stale
hardcoded download versions. Ask for any permissions required by the execution
environment before changing browser automation access or installing services.

```sh
agentcookie wizard install --as source --browser zen --peer <sink>
```

On a terminal the wizard asks for missing browser/companion/hold decisions.
For unattended operation provide explicit flags only after the user has agreed:
`--profile`, `--allow-companion`, and container choices or `--accept-held-sites`.
Review canonical profile identity, resolution `How`, install hash, reader mode,
catalog unread counts, cookie/storage counts and held names. No matched install
section means choose a profile; do not pick an arbitrary default.

Explain the temporary companion's permissions: cookies, contextualIdentities,
tabs, tabHide, webNavigation, scripting and all origins, plus the one-time
hiding-tabs notice. BiDi and the companion channel are loopback-only; the broker
owns the session across daemon restarts. Request-mode storage performs a
cookie-bearing GET per origin/read. Do not call it a zero-network mode.

Offer the wizard's printed `gecko-launcher install` command, with its explicit
BiDi port. It writes a launcher but does not launch/restart the browser. Ask the
user to quit the existing browser and start the launcher. Never kill a foreign
session, bypass a profile lock, or read databases of a running browser.

The wizard uses BiDi 9225 / companion 9226, separate from owned Chrome 9224.
Older reader configurations may default to BiDi 9224 / companion 9225; preserve
existing explicit settings and use matching launcher flags.

## Sink flow

```sh
agentcookie wizard install --as sink --destination live_chrome --peer <source> \
  --code <pairing-code> --pair-url <pairing-url>
```

`live_chrome` uses a dedicated directory and never the normal Chrome profile.
Preflight launches without a window, checks the CDP port, restores desired state
and requires ready before daemon installation. It stops the preflight-owned
browser before handing ownership to the daemon. `agentcookie Chrome.app` asks the
running sink to open the destination; first-window restoration is enforced again.
Linux `attach` expects an existing browser and prints daemon/supervisor guidance.

Existing configs are preserved unless explicitly migrated with `--force`.
Do not enable legacy SQLite/Keychain strategies for a live destination. Do not
claim systemd or a LaunchAgent was installed when only instructions were printed.

## Verify and report

Run `agentcookie doctor` and `agentcookie status --json` on both sides. Inspect
Source adapter/engine, catalog, held sites, Destination running/ready, accepted
versus applied generations, pending/failed records and repair-needed. Trigger a
fixture or user-authorized sync; no accepted reply alone proves the sink is
logged in. Check a user-approved non-sensitive site in the owned destination.

Report observed results and remaining failures. Never log cookie/token values,
run old compatibility binaries on a developer Mac, or reset replay/ownership
state to force a success. Lost source generation state needs authenticated
reseed; repair requires explicit acknowledgement of lost ownership limitations.
