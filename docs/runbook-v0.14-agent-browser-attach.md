# Runbook: v0.14 agent browsers + cookie-injector fix

This work set out to give Chromium agent browsers (browser-use, vercel-labs
agent-browser) your real Chrome login the same way agentcookie does for the
cross-machine sink and the cmux WebKit pane. Testing showed that goal is not
reliably achievable for hardened logins, and the durable win turned out to be
a correctness fix in the shared cookie injector. This runbook records both
honestly so the next person does not repeat the investigation.

## Finding: you cannot transplant a hardened login into a separate agent browser

Tested against GitHub (Chrome 148) with two transplant methods:

1. **CDP attach (`browser-use --cdp-url` / `agent-browser --cdp`).** These
   tools open an isolated browser context over CDP that does not inherit the
   connected Chrome profile's cookies. A session driven this way saw ~7
   cookies (only what it set itself), not the profile's thousands. GitHub
   read logged-out.

2. **Playwright `storage_state` snapshot.** agentcookie can emit a correct
   storage_state JSON from real Chrome (verified: `logged_in=yes`,
   `dotcom_user=mvanhorn`, a valid 48-char `user_session`). But when an agent
   browser loads it, Chromium accepts the non-httpOnly and session cookies
   and **drops the persistent + httpOnly session cookies** that actually
   authenticate (`user_session`, `__Host-user_session_same_site`). This is
   intentional browser hardening, not an agentcookie bug. `/settings/profile`
   redirected to login.

The discriminator was clean: cookies that are *both* persistent *and*
httpOnly were dropped on load; session or non-httpOnly cookies loaded fine.

### What works instead: same profile

The only reliable way to give a Chromium agent browser your real login is to
use the same profile, via the tool's own flag:

- `browser-use --profile Default` — native real-profile drive (full
  fidelity). Needs your normal Chrome **closed**; on Chrome 136+ enable
  `chrome://inspect#remote-debugging` once.
- `agent-browser --profile <dir>` — same idea.

agentcookie does not need to be in the loop for that path. The surfaces
agentcookie *does* carry reliably remain the sink and cmux.

## The shipped win: cookie-injector correctness fix

The investigation surfaced two real bugs in `internal/cdp` (the CDP cookie
injector the **sink** and **cmux** delivery already use):

1. **Domain on host-only / `__Host-` cookies.** `buildCookieParam` set a
   `Domain` attribute on every cookie. Chrome hard-rejects `__Host-` cookies
   that carry a `Domain`, and host-only cookies (host_key without a leading
   dot) should stay host-only. The fix sets `Domain` only for genuinely
   domain-scoped cookies (leading dot, non-`__Host-`) and enforces the
   `__Host-` mandates (Secure, Path `/`, no Domain).

2. **No flush on close.** `InjectCookies` cancelled the headless Chrome
   context (SIGKILL). Cookies set over CDP live in Chrome's in-memory store
   and only flush to SQLite on a clean shutdown, so the kill dropped cookies
   nondeterministically and left the profile SingletonLock held. The fix
   calls `chromedp.Cancel` for a graceful close that flushes and waits.

Verified: GitHub's host-bound login cookies (`user_session`, `dotcom_user`,
`__Host-user_session_same_site`) go from silently dropped to persisted (6 →
13 cookies in a seeded profile). This makes the sink's delivery faithful for
modern sites that use `__Host-`/host-only session cookies.

## Status of `agentcookie attach`

The `attach` command, its `--fallback` debug profile, the Chrome
version/policy gate, and the `doctor` reachability check remain in the binary
as scaffolding. They correctly seed a profile and detect Chrome's debugging
posture, but they do **not** reliably carry a hardened login into the agent
browsers (see the finding above). Treat them as experimental until/unless the
agent browsers expose a profile-context attach that inherits cookies, or
support an unhardened-cookie storage_state path.
