---
title: "feat: README scope refresh for v0.13/v0.14 secrets-bus era"
status: active
type: feat
created: 2026-05-23
---

# feat: README scope refresh for v0.13/v0.14 secrets-bus era

## Problem Frame

The current `README.md` was last rewritten on 2026-05-22 (PR #59) as a marketing-shaped pass. Two big PRs landed the same day that materially widened agentcookie's scope:

- v0.13.0-beta.1 (#61) shipped the secrets bus: per-CLI `secrets.env` files riding alongside the cookie sync, a new `agentcookie secret` subcommand, a Go reader library at `pkg/agentcookiesecret`, an updated doctor (11 categories), and the v1 format spec.
- v0.14.0-beta.1 (#62) shipped the adoption standard: `agentcookie.toml` manifests, auto-discovery via `agentcookie discover`, three integration tiers (explicit-manifest / pp-cli-derived / legacy-v1), and the author-side helper at `pkg/agentcookieadoption`.

The README still describes agentcookie as a cookie-sync tool. The product is now session-state sync: cookies AND the bearer tokens / API keys / per-CLI auth blobs that ride next to them. A first-time reader landing on the repo today gets an incomplete picture of what gets replicated and what they get for free when they install it.

Refresh the framing to cover session state (cookies + secrets), keep the cookie story dominant since that is still the headline value, and slot the secrets bus + adoption standard in as features. Don't over-rotate the doc around the new work — most of agentcookie is still the cookie sync engine.

## Scope

In scope:
- `README.md` only.
- Widen the intro framing from "cookies sync" to "session state sync (cookies + secrets)".
- Slot the secrets bus and adoption standard into the existing structure (How it works, Working list, Documentation table) without restructuring the doc.
- Update outdated numbers (test count, package count, doctor categories).
- Add documentation table rows for the v0.13/v0.14 specs and runbooks.

Out of scope:
- No changes to `docs/quickstart.md`, `docs/quickstart-beta.md`, or any runbook.
- No new marketing pass on the whole doc; existing tone and structure stay.
- No expansion of the "How it works" ASCII diagram into a multi-panel diagram — keep it scannable.

### Deferred to Follow-Up Work

- Quickstart-level walkthrough of the secrets bus from a user's perspective (currently lives in `docs/runbook-secrets-bus-adoption.md`; pulling it forward into the main quickstart is a separate doc task).
- Top-level project description on GitHub (the `gh repo edit --description` text) — refresh after the README lands.

## Requirements

- R1: A reader who has never seen agentcookie understands within the first paragraph that it syncs both cookies and per-CLI secrets, not just cookies.
- R2: The secrets bus and adoption standard each appear at least once in the "Working" status list as concrete features.
- R3: The "How it works" section acknowledges that the source ships secrets bus payloads alongside cookies in the same envelope, without redrawing the diagram from scratch.
- R4: The "Documentation" table cites the v1 secrets-bus spec, v2 adoption-standard spec, the adoption runbook, and the gh-shim worked example.
- R5: Numeric claims match reality at HEAD: `449+` tests across `26` packages, `11` doctor categories.
- R6: The cookie sync remains the visible headline — the install commands, the "What it looks like" examples, and the diagram stay cookie-centric; secrets get integrated into the surrounding context rather than taking over the page.

## Key Technical Decisions

- **Refresh in place, do not rewrite.** The README was rewritten 2 days ago; structure and tone are still good. Touch only the sections where v0.13/v0.14 changed the picture.
- **One "What it looks like" example stays, no secrets-bus example added.** The cookie examples (instacart carts, ebay auctions, table reservations) carry the headline because they're the user-visible payoff. Adding a `secrets.env` shell example would shift weight to the new feature. Mention secrets in the framing sentence right above or below the code block instead.
- **Extend the existing diagram, don't redraw.** The current ASCII diagram has the laptop -> tailnet -> sink shape. Add a second short arrow / line acknowledging the secrets bus rides the same envelope. No second diagram.
- **Working/Not yet are the right home for feature mentions.** This is where readers scan for "what does this actually do." That section gets the bulk of the secrets-bus content.

## Implementation Units

### U1. Reframe the intro

**Goal:** Widen the opening from "sessions" (currently shorthand for cookies) to "session state" — explicitly cookies + secrets — so a first-time reader gets the right mental model in the first scroll.

**Requirements:** R1, R6.

**Dependencies:** None.

**Files:** `README.md` (lines 1-6, and the "What it looks like" framing prose at lines 9 and 26-28).

**Approach:**
- Replace "keeps your second Mac's sessions in sync" with phrasing that names both cookies and per-CLI secrets (e.g., "keeps your second Mac's session state — cookies, bearer tokens, per-CLI auth blobs — in sync").
- Keep the "OpenClaw, Hermes, or any other agent runtime" sentence as-is.
- In the "What it looks like" intro sentence (currently "watches Chrome's Cookies file and ships the diff"), add a clause that the per-CLI secrets bus rides the same channel — one clause, not a paragraph.
- Do not change the three example commands. They carry the headline.

**Patterns to follow:** The voice of the existing intro (concrete, second-person, no jargon). Preserve sentence-level rhythm.

**Test scenarios:**
- Test expectation: none -- README copy verified by reading rendered output against R1 and R6.

**Verification:** A reader who knows nothing about v0.13/v0.14 should be able to answer "does this replicate API keys too, or just browser cookies?" from the first paragraph alone.

---

### U2. Extend "How it works" to acknowledge the secrets bus

**Goal:** Make the architecture section honest about what crosses the wire without redrawing the diagram.

**Requirements:** R3, R6.

**Dependencies:** U1 (intro framing sets up the reader for the diagram update).

**Files:** `README.md` (lines 38-71).

**Approach:**
- Add a short stanza to the source side of the ASCII diagram noting `secrets bus (~/.agentcookie/secrets/<cli>/secrets.env)` as a second watched surface feeding into the same encrypted push. Keep the diagram under ~30 lines total.
- Below the diagram, the "Three surfaces because different agents read cookies differently" paragraph stays but gets a follow-up sentence: per-CLI auth tokens land at `~/.agentcookie/secrets/<cli>/secrets.env` on the sink (mode 0600), and CLIs read them via env vars or the `pkg/agentcookiesecret` Go library.
- Do not introduce a fourth "delivery surface" — secrets are a parallel track, not a fourth cookie sink.

**Patterns to follow:** Existing ASCII conventions (column layout, arrow style). Match the casing and spacing.

**Test scenarios:**
- Test expectation: none -- diagram + prose verified against `CHANGELOG.md` v0.13/v0.14 entries and `docs/spec-agentcookie-secrets-bus-v1.md`.

**Verification:** The diagram still fits on one screen. A reader can trace cookie flow and secret flow without re-reading.

---

### U3. Refresh the "Working" / "Not yet" status lists

**Goal:** Make the at-a-glance feature list reflect what shipped yesterday.

**Requirements:** R2, R5.

**Dependencies:** None (independent of U1/U2 prose changes).

**Files:** `README.md` (lines 115-138).

**Approach:**
- Update the test/package counts to `449+ unit tests across 26 packages`.
- Mention `agentcookie doctor` now reporting 11 categories (currently the line lists what doctor checks; add secrets-bus health to it).
- Add Working-list bullets for: secrets bus delivery (per-CLI `secrets.env` files, sealed-optional twin, mode 0600); the `agentcookie secret` and `agentcookie discover` subcommands; the v2 adoption standard with three integration tiers.
- Adjust "Not yet" if anything previously listed is now shipped. The current "Not yet" list (more adapters, `pair --rotate`, fan-out, Linux/Windows, at-rest sealing default) is still accurate; verify before editing.
- Keep the section terse — one line per bullet.

**Patterns to follow:** Existing bullet voice (verb-first, no marketing fluff). The current list is the template.

**Test scenarios:**
- Test expectation: none -- numbers cross-checked via `go test ./... 2>&1 | tail -3` and `find . -name '*_test.go' | wc -l`; subcommand list verified against `internal/cli/`.

**Verification:** Every claim in the Working list maps to a feature that ships in `cmd/agentcookie` at HEAD.

---

### U4. Refresh the Documentation table

**Goal:** Surface the new specs and runbooks so a reader scanning the table can find the secrets-bus material.

**Requirements:** R4.

**Dependencies:** None.

**Files:** `README.md` (lines 140-153).

**Approach:**
- Add four rows for: `docs/spec-agentcookie-secrets-bus-v1.md` (v1 format spec), `docs/spec-agentcookie-secrets-bus-v2-adoption.md` (adoption standard), `docs/runbook-secrets-bus-adoption.md` (migration runbook), and `docs/runbook-secrets-bus-gh-example.md` (gh-shim worked example).
- Keep existing rows. Order new rows after the v0.12 runbook rows so the table reads roughly version-chronological.
- Optional: add a row for `examples/gh-shim/` if it reads naturally; skip if it doesn't fit the "doc / use" framing.

**Patterns to follow:** Existing two-column markdown table format. Path-as-link in column 1, short use-case phrase in column 2.

**Test scenarios:**
- Test expectation: none -- paths verified by `ls docs/`.

**Verification:** Every new row's path resolves to a file at HEAD.

## Scope Boundaries

- Touch only `README.md`. No edits to `docs/`, `CHANGELOG.md`, or `skill/SKILL.md`.
- No structural reorganization of the README. Sections stay in their current order; only content within sections changes.
- No removal of existing content unless it became factually false (numbers, missing subcommands).
- The "Install" and "Verify it's working" sections do not change — install flow is identical, and `verify-adapters` output still represents the cookie path which remains the headline.

## Verification

For the plan as a whole:
- Render the updated README locally (or via `gh repo view --web` after pushing a branch) and read it cold from top to bottom.
- Confirm R1-R6 are each satisfiable by a specific passage.
- Confirm the cookie sync remains the dominant story by word-count and example-count — the secrets bus is mentioned, not featured.
- Cross-check every numeric claim against the live build (test count, package count, doctor categories).
