# Runbook: v0.14 agent-browser attach

Make a Chromium agent browser (browser-use, vercel-labs agent-browser)
share your real Chrome session over the DevTools Protocol, so it is logged
in everywhere you are -- the fix for "are you sure you're logged in?" during
agent navigation and HAR sniffing.

This is distinct from the cmux loop. cmux is WebKit and cannot CDP-attach,
so it gets *injected* cookies (`agentcookie cmux-sync`). browser-use and
agent-browser are Chromium and *attach* to the real browser instead of
receiving a copy, which is the only way device-bound (DBSC) logins and
localStorage-token sessions come along.

## Why attach instead of copy

Copying cookies into a fresh agent-browser profile fails for two common
session types:

- **Device-bound (DBSC) cookies** cannot be replayed into another browser
  or profile. The binding key lives in the OS keystore, tied to the
  originating browser; a copied cookie row is rejected by the server.
- **Auth in localStorage/IndexedDB** (common in SPAs) is not a cookie and
  is not carried by a cookie copy.

Attaching to the real Chrome sidesteps both: there is one session.

## One-time Chrome setup (Chrome 136+)

Since Chrome 136, `--remote-debugging-port` is refused on the default
profile (a malware-hardening change). To attach your real profile you must
use the Chrome 144+ user-gated path:

1. Open `chrome://inspect#remote-debugging`.
2. Turn the remote-debugging toggle on.

That exposes a loopback CDP endpoint for your real profile without a wide
open command-line port. `agentcookie attach` prints these exact steps when
it cannot reach the endpoint on a Chrome 144+ install.

Check your Chrome version at `chrome://version` (or `attach --check`, which
reports the detected version and policy tier).

## Wire it

```bash
agentcookie attach              # wire every installed agent browser (default)
agentcookie attach --print      # show the endpoint + launch snippets, write nothing
agentcookie attach --check      # report reachability, policy tier, and per-target wiring
agentcookie attach --target browser-use --wire
```

`--wire` writes a launcher per agent browser at
`~/.agentcookie/agent-browser/<tool>-attached`. Running that launcher runs
the tool already attached:

```bash
~/.agentcookie/agent-browser/browser-use-attached open https://example.com
```

The launcher bakes the stable `http://127.0.0.1:<port>` endpoint, not the
per-session WebSocket URL (which changes on every Chrome restart), so it
keeps working across restarts as long as the `chrome://inspect` toggle
stays on.

Equivalent one-shot flags, if you'd rather not use the launcher:

- browser-use: `browser-use --cdp-url http://127.0.0.1:9222`, or
  `browser-use --connect` (auto-discover).
- agent-browser: `agent-browser --cdp 9222`, or `agent-browser
  --auto-connect`.

## The tradeoff

When attached to your real Chrome, the agent drives your **live** browser
session. Actions happen in the browser you use. While the debug endpoint is
open, any process running as your user can connect to it (see
`docs/threat-model.md`). Enable it when running agents; turn the
`chrome://inspect` toggle off when you're done. For an isolated session,
use the fallback below.

## Fallback: a synced debug profile

For Chrome older than 144, or when you want the agent browser isolated from
your live session:

```bash
agentcookie attach --fallback
```

This seeds a dedicated profile at `~/.agentcookie/chrome-debug` with your
default profile's cookies and localStorage, launches it on a loopback debug
port, and wires the agent browsers to it.

Limits of the fallback:

- **DBSC sessions do not transfer** to a separate profile -- a few sites may
  still read as logged-out. `attach --fallback` reports the skipped count.
- It is **one-shot**: it seeds and launches once. Continuous re-sync on
  every Chrome cookie change is a planned follow-up (it requires CDP-based
  re-injection to avoid a profile-lock conflict with the running debug
  Chrome).

## Configure defaults

`source.yaml` (flags override):

```yaml
agent_browsers:
  # targets:            # optional; default = all installed
  #   - browser-use
  #   - agent-browser
  # port: 9222          # optional; loopback debug port
```

## Verify

```bash
agentcookie attach --check     # endpoint + per-target wiring
agentcookie doctor             # includes the "agent browser attach" check
```

`doctor` reports whether Chrome is attachable on CDP and which agent
browsers are wired, and prints the remediation when something is off
(debugging not enabled, or a target not yet wired).
