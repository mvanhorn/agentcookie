---
title: Chrome 144+ chrome://inspect remote-debugging activation contract
status: verified-enough-for-u2
verified_chrome_version: 148.0.7778.168
verified_on: matts-mac-mini (macOS 15.3.1)
date: 2026-05-16
plan: docs/plans/2026-05-16-006-feat-attach-to-real-chrome-pivot-plan.md
unit: U1
---

# Chrome 144+ chrome://inspect remote-debugging activation contract

Research output for v0.5 U1. The goal is to pin down precisely how Chrome 144+'s
in-browser remote-debugging toggle exposes its CDP endpoint so U2 can connect
via chromedp without guessing.

## Bottom line

Chrome 144+ ships an in-browser switch at `chrome://inspect/#remote-debugging`
that activates the standard CDP listener on the running Chrome without
requiring `--remote-debugging-port`, `--user-data-dir`, or any other
command-line flag. The default profile is supported (this is the whole
point; it sidesteps the March 2025 hardening that blocks `--remote-debugging-port`
on default profiles).

The endpoint is the same DevTools Protocol endpoint that managed Chrome
exposes today, with the same `DevToolsActivePort` discovery file. The
difference is HOW the endpoint gets stood up (UI toggle vs flag at startup),
not WHAT the endpoint is.

## How clients connect

Verified by reading the Chrome DevTools MCP source
(`ChromeDevTools/chrome-devtools-mcp`, file `src/browser.ts`). The
`--autoConnect` code path:

1. Reads `<user-data-dir>/DevToolsActivePort` from the user's Chrome profile.
2. Parses two lines: line 1 is the port (decimal), line 2 is the browser-level
   WebSocket path (e.g. `/devtools/browser/<uuid>`).
3. Constructs `ws://127.0.0.1:${port}${wsPath}` and connects directly via
   the WebSocket. No HTTP `/json/version` round-trip is required.
4. The first connection from a new client triggers an in-browser permission
   dialog. The user must click Allow once per client.

The legacy `http://localhost:9222/json/version` HTTP probe is not the right
discovery path here. Even with chrome://inspect activated, the port is dynamic
(Chrome picks one) and may not be 9222. The HTTP enumeration surface may also
be gated (Issue
[ChromeDevTools/chrome-devtools-mcp#1194](https://github.com/ChromeDevTools/chrome-devtools-mcp/issues/1194)
reports failures using `-u http://localhost:9222` even when chrome://inspect
is enabled). Treat `DevToolsActivePort` as the authoritative discovery
artifact, not HTTP.

The relevant chrome-devtools-mcp code (paraphrased from `src/browser.ts:79-98`):

```typescript
const portPath = path.join(userDataDir, "DevToolsActivePort");
const [rawPort, rawPath] = fs.readFileSync(portPath, "utf8")
  .split("\n").map(s => s.trim()).filter(Boolean);
const browserWSEndpoint = `ws://127.0.0.1:${parseInt(rawPort, 10)}${rawPath}`;
await puppeteer.connect({ browserWSEndpoint });
```

## DevToolsActivePort file locations (macOS)

For the default Chrome profile (the user's everyday browser; what we want in
attach mode):

```
~/Library/Application Support/Google/Chrome/DevToolsActivePort
```

Note: the file lives at the user-data-dir root, not inside `Default/`. This
matches what we already observe for the v0.4 managed Chrome (which writes
its own DevToolsActivePort under `~/.agentcookie/chrome-profile/`).

Verified file format from the running v0.4 managed Chrome on Matt's Mac mini:

```
53747
/devtools/browser/fbaf41a0-51c2-4c3d-86c0-65a7e5db6035
```

Two lines, no trailing fluff. chrome://inspect-enabled default Chrome should
write this same file at the default-profile location on toggle activation.

## Permission dialog behavior

From the official Chrome blog
([chrome-devtools-mcp-debug-your-browser-session](https://developer.chrome.com/blog/chrome-devtools-mcp-debug-your-browser-session)):

> Every time the Chrome DevTools MCP server requests a remote debugging
> session, Chrome will show a dialog to the user and ask for their
> permission.

Interpretation: the dialog fires on initial WebSocket connect, not on every
CDP command. A long-lived connection grants the dialog once and stays alive.
For agentcookie sink (continuous-sync use case), this is fine: we open the
WebSocket once at sink start, hold it open, and write cookies as batches
arrive. The user sees the dialog the first time the sink boots and never
again until they restart Chrome.

Open question: does the permission grant persist across Chrome restarts, or
does each restart re-prompt? Either is workable (sink restart-reconnect logic
handles the transient case), but the wizard's user-facing copy needs to
match reality. Verify empirically in U2 by restarting Chrome on the Mac mini
and observing whether the sink reconnects without a dialog.

## Persistence of the toggle itself

The "Allow remote debugging for this browser instance" wording on the
chrome://inspect/#remote-debugging page is ambiguous on persistence. Two
plausible interpretations:

A. Persists across Chrome restarts. Stored as a Chrome preference (`Local
   State` or per-profile `Preferences`). Toggle once, attach mode keeps
   working forever.

B. Per-Chrome-instance only. Each Chrome restart clears the toggle, user
   has to flip it again.

Neither path was nailed down from public docs. Both should be tested in
U2 (start Chrome with toggle off; flip toggle; observe DevToolsActivePort
appears; quit Chrome; relaunch; observe whether DevToolsActivePort
reappears at launch or only after another toggle flip). The wizard's copy
adapts to whichever it is. If it's (B), wizard becomes a one-line "you
need to flip this whenever you restart Chrome" reminder, which is fine
since most users keep Chrome running.

For Matt specifically: Mac mini Chrome runs essentially 24/7 because PP CLIs
and agent runtimes need it. So even if persistence is (B), the practical
impact is minimal as long as the LaunchAgent re-prompts on Chrome restart.

## Security model

Verified from the Chrome blog and chrome-devtools-mcp source:

- Endpoint binds to `127.0.0.1` only (the `ws://127.0.0.1:...` URL pattern
  in MCP code is not a coincidence; the port is loopback-only).
- Outside-the-loopback traffic is rejected at the socket layer.
- The permission dialog gates the connection at WebSocket-accept time.
- Once accepted, the client has full CDP access including arbitrary
  cookie reads/writes and JavaScript execution in any tab.

Implication for agentcookie: the sink and Chrome MUST run on the same
machine. Cross-machine direct connections to the CDP port are not
supported. This matches agentcookie's existing architecture (sink runs on
the Mac mini, Chrome runs on the Mac mini, transport from laptop to Mac
mini happens via agentcookie's own AES-256-GCM channel on top of
Tailscale).

## Chrome version requirement

- 144 (Beta channel only): autoConnect available but unstable.
- 146 (Stable): first stable release with autoConnect end-to-end.
- 148 (Matt's current): well above requirement.

Per the chrome-devtools-mcp guide reference
([heyuan110.com setup guide](https://www.heyuan110.com/posts/ai/2026-03-17-chrome-devtools-mcp-guide/))
and Chrome DevTools 144 release notes
([new-in-devtools-144](https://developer.chrome.com/blog/new-in-devtools-144)),
attach mode against the default profile is a Chrome 144+ feature; agentcookie
v0.5 sets the floor at 144 for parity with chrome-devtools-mcp's
`--autoConnect`.

U4's wizard should detect Chrome version via the `/json/version` endpoint
(once attached) or via `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome --version`
(pre-attach, to avoid the chicken-and-egg of needing CDP to check whether
CDP works).

## Discovery algorithm for U2

Given the above, U2's `chromeconn.Discover()` should:

1. Resolve the default Chrome profile path:
   `~/Library/Application Support/Google/Chrome/` on macOS.
2. Read `DevToolsActivePort` from that directory.
3. If missing: return a typed error `ErrRemoteDebuggingNotEnabled`. Wizard
   surfaces the "open chrome://inspect/#remote-debugging" instruction.
4. If present: parse port + wsPath, construct `ws://127.0.0.1:${port}${wsPath}`.
5. Open WebSocket. If the connection is rejected at the dialog phase (user
   clicked Deny, or dialog timed out), return `ErrPermissionDenied`.
6. Otherwise, return the `*chromedp.RemoteAllocator` (or its underlying
   connection) ready for cookie operations.

Watch out for:

- File-not-found is the dominant "not enabled" signal. Don't try to probe
  HTTP first.
- The file may be stale (Chrome was running, crashed, the port from
  DevToolsActivePort is now occupied by something else). Mitigation: open
  the WebSocket immediately after reading the file. If it errors with
  ECONNREFUSED, treat the file as stale and surface a clear error.
- Multiple Chrome user profiles. v0.5 targets the Default profile; non-Default
  profile support is v0.6 territory.

## Open questions deferred to U2

- Does the permission grant persist across Chrome restarts, or fire every time?
- Does the chrome://inspect toggle itself persist (preferences vs. per-instance)?
- Does the dialog identify the connecting client by name? If so, how? (The
  wizard's UX text adapts to whatever the dialog says.)
- Is there a graceful re-prompt path if the user later clicks Deny by mistake,
  or do they need to re-toggle the inspect page?

None of these block U2 implementation. They're empirical questions answered by
the first real connection on Matt's Mac mini.

## Sources

- [Chrome 144 DevTools release notes](https://developer.chrome.com/blog/new-in-devtools-144)
- [Chrome DevTools MCP debug your browser session blog](https://developer.chrome.com/blog/chrome-devtools-mcp-debug-your-browser-session)
- [ChromeDevTools/chrome-devtools-mcp source (browser.ts autoConnect path)](https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/src/browser.ts)
- [chrome-devtools-mcp issue #1194: localhost:9222 fails with chrome://inspect](https://github.com/ChromeDevTools/chrome-devtools-mcp/issues/1194)
- [chrome-devtools-mcp issue #1826: DevToolsActivePort fallback for Edge](https://github.com/ChromeDevTools/chrome-devtools-mcp/issues/1826)
- [heyuan110.com: Chrome DevTools MCP setup 2026](https://www.heyuan110.com/posts/ai/2026-03-17-chrome-devtools-mcp-guide/)
- [Scalified: Chrome DevTools MCP authentication](https://scalified.com/blog/chrome-devtools-mcp-authentication)
- [Chrome enterprise policy: RemoteDebuggingAllowed](https://chromeenterprise.google/intl/en_ca/policies/remote-debugging-allowed/)
