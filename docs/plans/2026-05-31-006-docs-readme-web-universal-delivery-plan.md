---
title: "docs: refresh README + agentcookie.dev for verified universal cookie delivery"
type: docs
status: active
date: 2026-05-31
repo: agentcookie
origin: "follows PR #71 (race + doctor + live verification). The keychain model shipped past what the README and marketing site describe; this audits both surfaces against current behavior and updates them. No printing-press scope."
---

# docs: refresh README + agentcookie.dev for verified universal cookie delivery

## Summary

The product moved and the docs did not. agentcookie now delivers universal cookie access on a sink: one macOS login-password entry on the install terminal, zero GUI clicks, and any unmodified cookie tool (yt-dlp, gallery-dl, the Printing Press CLIs, a browser-driving agent) reads the real synced, logged-in Chrome profile. This was verified live on macOS 15.3.1 on 2026-05-31 (partition set + verified readable, the daemon wrote the real Default profile with 8875 cookies, a security-CLI read returned the key) and the duplicate-item race that used to block it is fixed (PR #71).

Both the GitHub README (`README.md`) and the agentcookie.dev marketing site (`web/`) still describe the older v0.12 keychain model: per-binary `-T` ACLs, "three cookie surfaces" as the headline, "skips Chrome Safe Storage on headless installs," and stale counts (466 tests, 12 doctor categories). This plan audits both surfaces against the shipped behavior, then updates them so a reader sees what agentcookie actually does today, with the README done first.

This is documentation only: no product code changes, no behavior changes. The work is an accuracy pass plus elevating universal delivery to the headline it now deserves.

Target repo: agentcookie. Scope is the README and agentcookie.dev only. printingpress.dev is explicitly out of scope.

---

## Problem Frame

### What changed in the product that the docs miss

- Universal cookie delivery is the new default: a fresh sink writes the real Default Chrome profile and grants the Safe Storage key via the one-password partition (`apple-tool:,apple:,teamid:<team>`), so any unmodified cookie CLI works. The README never uses the words "universal," "one password," or "any unmodified cookie CLI"; its keychain line still says per-binary `-T` ACL (README "Status" + "What this fixes").
- The "three cookie surfaces" framing (Chrome SQLite / plaintext sidecar / per-CLI adapter) is now the implementation detail beneath universal delivery, not the headline. The real headline is "your logged-in browser session, usable by anything on the sink, with one password."
- The duplicate-item race and its converge fix, and the honest locked-SSH / unsigned-CGO boundaries, are new truths worth stating plainly (and they correct the old "no GUI clicks at all" claim, which overpromised).
- Counts drifted: README says "466+ unit tests across 26 packages" (now 526+) and doctor "12 health categories" (now 14, including Cookie delivery). Version-dated "Not yet" lines (e.g. Python reader "queued for v0.13.1") may also be stale.

### Why both surfaces, README first

The README is the canonical first read for anyone landing on the repo and is the higher-priority, higher-traffic surface, so it goes first. The marketing site repeats the same claims in Hero / FeatureGrid / FAQ / Terminal copy and `lib/features.ts`, with copy assertions in `page.test.tsx` that will need to track the new wording. Doing the README first establishes the canonical phrasing the site then mirrors.

---

## Requirements

- R1. A reader of `README.md` learns, near the top, that agentcookie delivers universal cookie access: one login-password entry, zero GUI clicks, any unmodified cookie CLI reads the synced logged-in Chrome profile on the sink.
- R2. Every stale keychain claim in the README is corrected: the per-binary `-T` ACL line becomes the one-password partition (`apple-tool:/apple:/teamid:`); the "skips Safe Storage on headless" line becomes the one-password universal default with the degraded fallback as the explicit opt-out.
- R3. The README states the honest boundaries that shipped: the one-password (not zero-interaction) reality, the duplicate-item race + auto-converge, the locked-SSH read behavior, and the unsigned-CGO (`apple-tool:`/`teamid:` only) limit.
- R4. Drifted counts and version-dated lines are refreshed (test count, doctor category count, any stale "Not yet" version tags) or made version-agnostic so they stop drifting.
- R5. The audit is explicit: a single list of every location across README + site that asserts the old model, so nothing is silently missed.
- R6. The agentcookie.dev marketing site (Hero, FeatureGrid, FAQ, Terminal, `lib/features.ts`) reflects the same universal-delivery headline and corrected claims, and its copy tests (`page.test.tsx`) pass against the new wording.
- R7. No product code or behavior changes; documentation and marketing copy only.

---

## Implementation Units

### U1. Audit both surfaces against shipped behavior

**Goal:** Produce the authoritative list of every place in `README.md` and `web/` that asserts the old keychain/delivery model or carries a drifted count, so U2/U3 update from a checklist rather than ad hoc.

**Requirements:** R5.

**Dependencies:** none.

**Files:**
- `README.md` (read)
- `web/app/(marketing)/page.tsx`, `web/components/marketing/*.tsx`, `web/lib/features.ts`, `web/app/(marketing)/page.test.tsx` (read)
- Ground truth to audit against: `docs/runbook-v0.13-one-password-keychain.md`, `internal/cli/doctor.go` (current check list + count), `internal/cli/wizard.go` / `internal/cli/wizard_keychain.go` (universal default + one-password path), the live-verification notes in `docs/plans/2026-05-31-005-...-plan.md`.

**Approach:** Grep both surfaces for the stale markers — `-T`, "any application", "three cookie surfaces", "no GUI clicks", "skips", "466", "12 health", "v0.13.1", "--any-app", per-binary trust list — and cross-check each hit against the v0.13 runbook and current code. Capture each finding as (file, current claim, correct claim). Count current tests (`go test ./... | tail`) and doctor categories (read `doctor.go`) so U4 uses real numbers.

**Test scenarios:** `Test expectation: none -- audit/inventory step; the deliverable is the findings checklist consumed by U2 and U3.`

**Verification:** A written checklist enumerating every stale claim across README + site with its correction, plus the verified current test count and doctor-category count.

---

### U2. Update README.md (done first)

**Goal:** Make the README accurately and prominently describe universal cookie delivery, correct every stale keychain claim, and refresh counts.

**Requirements:** R1, R2, R3, R4, R7.

**Dependencies:** U1.

**Files:**
- `README.md` (modify)

**Approach:** Lead with the universal-delivery capability (one password, zero GUI clicks, any unmodified cookie CLI reads the real logged-in Chrome profile), keeping the existing voice and the laptop-to-second-Mac framing. Reframe the "three cookie surfaces" section as the mechanism beneath universal delivery rather than the headline. Replace the per-binary `-T` ACL lines with the one-password partition (`apple-tool:,apple:,teamid:<team>`). Replace "skips Safe Storage on headless" with the one-password universal default and the explicit degraded opt-out. Add a short, honest boundaries note (one-password not zero-interaction; duplicate-item race auto-converged; locked-SSH read; unsigned-CGO `apple-tool`/`teamid` limit). Refresh the test count and doctor-category count to the U1-verified numbers, and either correct or de-version the stale "Not yet" lines. Point the keychain doc link at `docs/runbook-v0.13-one-password-keychain.md`.

**Patterns to follow:** the README's existing register (direct, second-person, fenced terminal examples). No emdashes/bold churn beyond what's already there.

**Test scenarios:** `Test expectation: none -- prose documentation. Verification is the U1 checklist fully resolved against README.md.`

**Verification:** Every README item on the U1 checklist is corrected; the README names universal delivery near the top; counts match U1; no per-binary `-T`-as-current-model claims remain.

---

### U3. Update the agentcookie.dev marketing site

**Goal:** Bring the marketing copy in line with the README: universal-delivery headline, corrected keychain claims, and passing copy tests.

**Requirements:** R6, R7.

**Dependencies:** U1, U2 (so the site mirrors the README's canonical phrasing).

**Files:**
- `web/components/marketing/Hero.tsx`, `web/components/marketing/FeatureGrid.tsx`, `web/components/marketing/FAQ.tsx`, `web/components/marketing/Terminal.tsx`, `web/components/marketing/WhatItSyncs.tsx`, `web/components/marketing/SecretsBusTile.tsx` (modify as the audit flags)
- `web/lib/features.ts` (modify: feature list copy)
- `web/app/(marketing)/page.tsx` (modify if it carries inline copy)
- `web/app/(marketing)/page.test.tsx` (modify: update copy assertions to the new wording, test)

**Approach:** Apply the U1 site findings: surface universal delivery (one password, any cookie CLI, real logged-in Chrome on the sink) in the Hero and/or FeatureGrid, correct any keychain/`-T`/"three surfaces"/"no clicks" copy, and update `lib/features.ts`. Update `page.test.tsx` assertions to match the new copy (these tests pin marketing wording, so they move with the copy). Keep the existing visual design and component structure unchanged; copy only.

**Patterns to follow:** existing component copy style and the assertion shape already in `page.test.tsx`.

**Test scenarios:**
- Updated copy assertions in `page.test.tsx` pass against the new wording.
- No assertion still pins a removed stale phrase (e.g. an old keychain claim).
- `Covers R6.` The site renders/builds with the universal-delivery copy present.

**Verification:** `web` build succeeds and `page.test.tsx` passes with the new copy; the site's headline and feature copy match the README's universal-delivery framing; no stale keychain claims remain in the marketing components.

---

## Scope Boundaries

### Deferred to Follow-Up Work

- printingpress.dev (`~/printingpress-dev`) — explicitly out of scope for this plan.
- Any product code, behavior, or new screenshots/diagrams beyond copy accuracy.

### Out of scope

- Re-architecting the marketing site, visual redesign, or new pages.
- Changing the keychain behavior itself (shipped in PR #71).

---

## Risks and Dependencies

- **Copy tests pin exact phrasing.** `page.test.tsx` asserts marketing strings; changing copy without updating the assertions breaks the build. Mitigation: U3 updates copy and assertions together.
- **Over-promising again.** The old "no GUI clicks at all" line overstated. Mitigation: R3 requires the honest one-password (not zero-interaction) framing on both surfaces.
- **Count drift recurs.** Hardcoded counts go stale. Mitigation: U4-style numbers come from U1's live count, and prefer de-versioning where a number adds little.

---

## Sources and Research

- Shipped behavior: PR #71 (`9e87c05`), `docs/runbook-v0.13-one-password-keychain.md` (one-password partition, verified-live section, duplicate-item race, unsigned-CGO boundary), `internal/cli/doctor.go` (current Cookie delivery check + category count), `internal/cli/wizard.go` / `wizard_keychain.go` (universal default + converge).
- Surfaces to update: `README.md`; `web/app/(marketing)/page.tsx`, `web/components/marketing/*.tsx`, `web/lib/features.ts`, `web/app/(marketing)/page.test.tsx`.
- Live verification: macOS 15.3.1 on moltbot-mini, 2026-05-31 (recorded in plan 005 and the v0.13 runbook).
