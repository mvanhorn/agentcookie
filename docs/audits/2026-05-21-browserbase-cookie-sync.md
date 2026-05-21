# Audit: Browserbase cookie-sync — what's worth being inspired by

**Date:** 2026-05-21
**Source:** https://github.com/browserbase/skills/blob/main/skills/cookie-sync/SKILL.md
**Verdict:** Adopt nothing right now. Reference doc only.

## Why this exists

After the v0.12.0-beta.5 dry-run validated the headless click-free install, Matt asked whether anything in Browserbase's `cookie-sync` skill was worth being inspired by for agentcookie. This document captures what I read, how it differs from agentcookie, and the explicit recommendation NOT to build any of it — yet.

The institutional knowledge: we looked, we understood, we decided. If a future iteration revisits, start here instead of re-reading Browserbase from scratch.

## What Browserbase's cookie-sync does

A one-shot Node script that:

1. Reads cookies from local Chrome via CDP (`--remote-debugging-port=9222`).
2. Pushes them into a Browserbase **persistent context** — a cloud-side identity blob that scheduled jobs can attach to.
3. Supports `--domains foo.com,bar.com` to filter sync by base+subdomain.
4. Supports `--context <ctx_id>` to refresh cookies in an existing context.
5. Supports `--verified` to enable Browserbase Identity (fingerprint-resistant remote browser).
6. Supports `--proxy "City,ST,Country"` to route through a residential proxy.
7. Workflow: sync locally → `browse cloud sessions create --context-id <ctx-id>` from anywhere to act as you.

## How agentcookie differs

| Dimension | Browserbase | agentcookie |
|---|---|---|
| Destination | Cloud-hosted browser session | Your own Mac mini |
| Identity unit | Persistent context (cloud blob) | Continuous source→sink sync to a real Chrome profile |
| Cookie source | CDP read from running Chrome | SQLite read from Chrome's Cookies file + Safe Storage decrypt |
| Network | Browserbase API over HTTPS | Tailscale peer-to-peer |
| Headline value | "Remote browser acts as me" | "Headless Mac mini acts as me via PP CLIs" |
| Cost model | Metered cloud sessions | Free; your hardware |

Different problems. The interesting question is whether any of Browserbase's mechanics map usefully.

## Feature-by-feature audit

| Browserbase feature | Maps to agentcookie? | Verdict |
|---|---|---|
| CDP-based source-side read | Direct parallel; eliminates source Safe Storage Keychain dependency | **Defer.** Real architectural alternative but no observed friction. |
| Domain filtering (`--domains`) | Direct parallel; agentcookie has only opt-out blocklist | **Defer.** No user demand; blocklist starter already covers privacy. |
| Context refresh by ID | agentcookie's continuous-watch model covers this implicitly | Skip |
| Verified browser / fingerprint resistance | N/A — sink is real Chrome owned by friend | Skip |
| Residential proxy / geolocation | Already covered by Tailscale exit-node hint | Skip |
| Persistent context as identity unit | Replaced by agentcookie's continuous sink-Chrome profile | Skip |
| `--persist` flag (write-back from session to context) | Real drift problem; sink-generated cookies never reach source | **Defer.** No observed friction. |

## The three deferred candidates, with rationale

### Domain allowlist (opt-in sync)

**For:** Cleaner privacy framing; Browserbase ships it. Small to build.

**Against:**
- `blocklist.yaml` already ships pre-populated with banking, password managers, tax, brokerage. The privacy case is solved with sensible defaults.
- Footgun: friend allowlists `instacart.com` and the Instacart adapter mysteriously misses cookies on `cdn.instacart.com` / `instacartstatic.com`. We just spent two days fixing exactly that flavor of "looks right, actually breaks" silent failure in CDP injection.
- The 5 built-in adapters already define an implicit allowlist via `CookieHostPatterns`. Adding a user-facing allowlist creates two sources of truth.
- Zero user demand. No dry-run surfaced a case where the blocklist felt insufficient.

**Revisit when:** a friend reports the blocklist feels insufficient or asks for an explicit allowlist by name.

### CDP-based source-side read

**For:** Symmetric with the sink-side click-free work. Eliminates the source-side Keychain prompt. Architecturally cleaner: no Safe Storage decrypt, no App-Bound prefix handling.

**Against:**
- The source-side Keychain prompt fires ONCE, on the source-side keyboard, where the friend is. It's a one-click setup step, not the Screen Sharing trip to a headless Mac that the sink-side work eliminated.
- `--remote-debugging-port` is an unauthenticated local socket. Any process on the source machine can read all cookies via that port. The current Safe Storage path requires a process matching the binary's signed identity. CDP is strictly less secure on the source side.
- Browserbase's `--user-data-dir=/tmp/chrome-debug` defeats the value prop ("sync from MY Chrome"). To preserve it we'd have to launch the friend's actual profile with the debug port, which conflicts with a double-clicked Chrome over the same profile dir.
- Two source modes doubles the support surface.
- Zero user demand.

**Revisit when:** Chrome 150+ breaks our direct SQLite read, or a friend reports the source-side Keychain prompt as friction.

### Two-way sync / write-back

**For:** Real session-drift theoretical problem — PP CLI does OAuth refresh on sink, sink's session is newer than source's, friend opens Chrome on laptop and sees themselves logged out of a site that's actually still authenticated on the Mac mini.

**Against:**
- We have not observed this drift in any dry-run. It's a hypothesis.
- Even if it happens, the next time the friend uses the site in their laptop Chrome, the site issues a new refresh cookie, source's Chrome stores it, source pushes to sink — drift resolves. Window is bounded.
- Conflict-resolution semantics are nontrivial. "Newer wins by timestamp" sounds simple but Chrome doesn't update `last_update_utc` predictably across subdomain contexts.
- The source-side Chrome is the conceptual source of truth. Reverse writes invert that mental model. The agent silently overwriting the human's browser state is surprising and not obviously what the friend wants.
- Substantial design surface; no friend has reported the underlying drift.

**Revisit when:** a friend reports "my laptop says logged out but my Mac mini's PP CLI works fine."

## What we'd build instead

If we have build budget right now, the items the dry-runs actually surfaced (not the items Browserbase suggests by example):

- **Friction #19** (loudest noise during install): v0.10 keychain access strategy loop fires on headless installs even though `skip_chrome_sqlite` is set, times out 60s per loop iteration, prints alarming WARNING lines that a friend will misread as install failure. Trivial to skip when sink is in headless mode.
- **Friction #21**: CDP injection drop rate (55% global, 94% on instacart). Investigation: why is Chrome's `Network.setCookies` silently rejecting cookies even with our URL+SameSite+normalized-Domain fixes? Sink-Chrome affordance is best-effort until this is understood.
- **Friction #22**: Source still announces Bonjour hostname (`MacBook-Pro-8.local`) in pair output. Friends copy it cross-LAN, sync breaks. Auto-detect Tailscale name when present.
- **Friction #23**: `agentcookie version` reports `0.0.1-dev` regardless of tag. Misleading. Inject version via `-ldflags` at build time.

## Recommendation

Build none of the three Browserbase-inspired candidates today. Keep this doc as the reference answer for "did we consider X?" Iterate from observed friction, not from feature parity with a competitor whose problem is different from ours.

## Operating principle this audit reinforces

agentcookie's value prop is "your Mac mini acts as you for PP CLI workloads." Cookies are the input; PP CLI session files are the output. Anything that improves THAT pipeline matters. Anything that mimics a cloud-browser-as-a-service company's surface area without first showing up as friction in agentcookie's own dry-runs is premature.

If a future iteration considers any of these features again, this doc should be the first read.
