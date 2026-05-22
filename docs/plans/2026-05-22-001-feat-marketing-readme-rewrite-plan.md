---
title: "feat: Marketing README + repo description rewrite"
status: active
type: feat
created: 2026-05-22
---

# feat: Marketing README + repo description rewrite

## Problem Frame

The current README is technically accurate and reasonably structured, but it's developer-first prose for a project whose actual audience is "someone running OpenClaw, Hermes, or similar agent runtimes on a Mac that isn't their primary machine, while their primary machine is also a Mac." That audience wants the outcome upfront: their agents act as them on every site they're logged in to, without per-site auth ceremony. They don't need an architecture diagram in the first scroll.

Two ambient problems compound the rewrite:

1. The history rewrite that removed a competitor reference from git history left two text-replacement corruptions in the live docs (`README.md` line 41, `docs/quickstart.md` line 125). Those have to be fixed in the same pass.
2. The current README's framing puts PP CLIs as the headline. The actual headline is broader: agentcookie keeps your agent's sessions in sync regardless of how the agent consumes them. PP CLIs are one consumer; browser-automation runtimes (OpenClaw, Hermes, Playwright, Puppeteer, chromedp directly) are another. The README should frame the product around the outcome, not one delivery channel.

## Summary

Rewrite `README.md` to lead with the agent-runtime-agnostic outcome ("your agent is logged in on the sink, automatically"), keep the working terminal demo as the second-scroll proof, push the architecture and the deep technical surface below the fold, drop the closed-beta banner from the top entirely, and fix the two text-corruption artifacts. Update the GitHub repo description in lockstep. No mention of competitor products or specific comparisons.

## Audience

The target reader is:

- Running an agent runtime (OpenClaw, Hermes, or similar) on a secondary Mac (typically a Mac mini they leave running headless).
- Has a primary Mac where they actually browse, log in, and authenticate sites.
- Knows what Tailscale is, knows what SSH is, is comfortable with `go install`.
- Does NOT want to log into every site twice (once on their laptop, once on the agent's Mac).
- Wants the agent to "just work" against authenticated sites without per-site adapter ceremony.

This is technical enough to read code blocks but oriented around the outcome, not the protocol.

## Requirements

- R1. README opens with the outcome statement (agent on a secondary Mac is logged in to everything the primary Mac is logged in to), not the mechanism.
- R2. README uses agent-runtime-agnostic language. PP CLIs appear as ONE example consumer, alongside browser-automation runtimes and direct HTTP usage. Not the headline.
- R3. No mention of closed beta in the README banner, header, or first scroll. The repo is private; access-controlled readers already know.
- R4. No reference to specific competitor products or named external skills/services in the README or repo description.
- R5. Working terminal session block stays as proof of value, near the top but after the outcome statement.
- R6. Architecture + protocol + status sections stay (or get tightened), pushed below the marketing-shaped header.
- R7. GitHub repo description rewritten to match the new framing (one to two sentences, no beta mention, no competitor reference).
- R8. The two text-replacement corruptions in `README.md:41` and `docs/quickstart.md:125` are fixed in the same pass with their semantically-correct original wording.
- R9. No em-dashes, en-dashes, or bold formatting in the new README content. Single hyphens are fine. (Honors maintainer formatting preference.)

## Scope Boundaries

In scope:
- `README.md` rewrite, full file.
- GitHub repo "About" description.
- Fix `docs/quickstart.md` line 125 corruption.

### Deferred to Follow-Up Work

- Animated GIF demo embedded in the README. Higher production cost; the existing terminal block carries the proof.
- Public landing page outside GitHub. Requires positioning + visual design work beyond a README pass.
- Reframing of `docs/quickstart-beta.md` and other docs to remove "beta" references throughout. Closed beta exists as an actual lifecycle stage; only the README banner is being removed for marketing reasons. Other docs stay accurate to internal state.

## Key Decisions

- **Lead with outcome, not protocol.** The first 100 words describe what the reader gets, not how it works. The how-it-works diagram and Tailscale/AES details stay, but below the fold.
- **Agent-runtime-agnostic framing.** The pitch is "your agent on the sink is logged in." How that agent uses the cookies (browser automation, CLI tool, direct HTTP) is the agent's choice. agentcookie hands over cookies in three shapes (Chrome's encrypted SQLite, plaintext sidecar, per-CLI session files) and lets the agent pick.
- **Drop the beta banner.** The repo is private; everyone who can read it already knows the access model. The banner adds friction and zero information for current readers.
- **No external comparisons.** Don't reference competitor products, even generically as "unlike X." The product stands on its own description.
- **Keep the existing terminal demo.** It's already concrete and outcome-oriented. Move it up under the new outcome lede.
- **Tighten the "Status" section** but keep it. Honest about Mac-only and the not-yet items. This is what an outcome-focused reader still wants before they install.
- **GitHub repo description is one to two sentences max.** It shows up in search results and in the sidebar; brevity matters. Mirror the README's outcome framing.

## High-Level Technical Design

Mental model for the new README's reading order:

```
[ Outcome lede - 2 to 3 sentences ]
   |
[ Concrete demo - terminal block showing agent on sink running cmds, returning real data ]
   |
[ Why this exists - 1 paragraph on the gap and what fills it ]
   |
[ How it works - existing diagram, lightly trimmed ]
   |
[ Install - existing two-command flow, no changes ]
   |
[ Status - working today + not yet, tightened ]
   |
[ Documentation index - existing table ]
   |
[ License ]
```

This illustrates the intended reading order and is directional guidance for the implementer; the exact section names and prose come from the rewrite itself, not this sketch.

## Implementation Units

### U1. Fix text-replacement corruptions

**Goal:** Restore the semantically correct wording in `README.md:41` and `docs/quickstart.md:125`, which the history rewrite mangled.

**Requirements:** R8.

**Dependencies:** none.

**Files:**
- `README.md` (line 41 area)
- `docs/quickstart.md` (line 125 area)

**Approach:** Read both lines, choose replacement wording that fits the surrounding context. For `README.md:41`, the original was likely "Existing cookie-sync tools" (generic descriptor) which got corrupted to "Existing that skill tools"; replace with a generic alternative such as "Tools that ship cookies between machines" or similar phrasing that doesn't reference the competitor and doesn't reintroduce the literal phrase that triggered the original concern. For `docs/quickstart.md:125`, the cron log filename "~/.agentthat skill.log" is clearly broken; restore to a sensible filename like `~/.agentcookie/source-cron.log`.

**Patterns to follow:** existing tone of surrounding paragraphs. No competitor names, no awkward phrasing introduced.

**Test scenarios:**
- happy path: `grep -r "that skill" --include="*.md"` returns empty after the fix.
- happy path: the cron example in `docs/quickstart.md` is syntactically valid and the log path points somewhere plausible under `~/.agentcookie/`.
- regression: no new mentions of the prior competitor's name are introduced.

**Verification:** the grep above returns empty; the cron example reads naturally.

---

### U2. Rewrite README.md

**Goal:** Replace the current README with the marketing-shaped version that matches the audience and framing decisions in this plan.

**Requirements:** R1, R2, R3, R5, R6, R9.

**Dependencies:** U1 (so the rewrite doesn't accidentally preserve the corrupted line).

**Files:**
- `README.md`

**Approach:** The rewrite keeps the existing structural skeleton (lede → demo → why → how → install → status → docs → license) but reshapes the first three sections to match the new framing. The outcome lede uses agent-runtime-agnostic language, naming OpenClaw / Hermes / Playwright / Puppeteer / chromedp as examples of consumers without making any one of them the headline. The "what it actually does" section keeps the existing terminal demo but reframes the surrounding prose to lead with "your agent" instead of "the CLI." The "why this is hard" section gets generic-ified to avoid the competitor reference entirely. Install, Status, Documentation, and License stay in place with light edits to remove the closed-beta banner and tighten phrasing.

The "Status" section keeps the honest "working today / not yet" split but rephrases the not-yet items in the outcome reader's language: "more agent-runtime adapters," "Linux and Windows source/sink," "live key rotation."

No em-dashes, en-dashes, or bold formatting anywhere in the new content. Single hyphens are fine.

**Patterns to follow:** the existing terminal demo block (concrete, multi-CLI, real output) sets the bar for proof. The existing diagram in "How it works" is good shape; keep its essence and tighten.

**Test scenarios:**
- happy path: `grep -ci "beta" README.md` returns zero (no beta mentions in the rewrite).
- happy path: `grep -c -- "--" README.md` returns no matches for en-dashes; same for em-dashes.
- happy path: `grep -c "\*\*" README.md` returns zero (no markdown bold).
- happy path: README still references the docs in the table (`docs/quickstart.md`, `docs/architecture.md`, etc.) so reader navigation works.
- happy path: README opens with an outcome sentence (something like "Your agent on the second Mac is logged in to everything you're logged in to on the first one"), not a technical claim about cookies.
- regression: terminal demo block still works as a paste-able example readers can see.

**Verification:** all grep checks above pass; the file reads top-to-bottom as a marketing piece that lands the value before the architecture; documentation links resolve to existing files in the repo.

---

### U3. Update GitHub repo description

**Goal:** Replace the current repo "About" description (the short tagline in the sidebar) with one or two sentences matching the new framing.

**Requirements:** R3, R4, R7.

**Dependencies:** U2 (so the description is consistent with the README lede).

**Files:**
- GitHub repo metadata (not a file in the repo; updated via `gh repo edit --description "..."`).

**Approach:** Compose a one to two sentence description. Drop the existing description's reference to PP CLIs as the value prop. Aim for a sentence that reads as the README's lede compressed. No beta language. No competitor reference. The description appears in GitHub search results and the social-card preview; brevity and clarity matter more than completeness.

**Patterns to follow:** the existing description's shape (a brief outcome statement) is the right form. Replace its content, keep its length.

**Test scenarios:**
- happy path: `gh repo view --json description --jq .description` after update returns the new text and contains no beta language.
- happy path: the description fits visually in GitHub's sidebar width without truncation (rough rule: 250 to 350 characters max).
- regression: no competitor or named-skill references appear.

**Verification:** `gh repo view --json description` returns the new description; visit the repo page and confirm the sidebar reads cleanly.

---

### U4. Verify documentation links + final pass

**Goal:** Confirm the rewrite did not break any in-repo links and the README still indexes the docs that exist.

**Requirements:** R6.

**Dependencies:** U2.

**Files:**
- None new; review-only across `README.md` and the `docs/` directory it references.

**Approach:** Walk every link target in the rewritten README's docs table and confirm the target file exists. Spot-check one or two other docs (architecture, threat-model) for any awkward references back to the old README phrasing that the rewrite may have orphaned. This is a fast review, not a rewrite of every doc.

**Test scenarios:**
- happy path: every relative link in the rewritten README resolves to an existing file in the repo.
- regression: the `docs/quickstart.md` corruption from U1 stays fixed (no residual "that skill" after U2 + U4).

**Verification:** every README link points at an existing file; the final grep for the prior corruption strings is still empty.

---

## System-Wide Impact

- Public-facing copy on the GitHub repo page shifts from "PP CLI session sync" to "agent session sync, runtime-agnostic." Anyone who was sharing the repo expecting the PP-CLI-specific framing should be told (Matt's circle). Nothing else downstream depends on the README wording.
- `docs/quickstart-beta.md` still exists and still references beta. That's intentional and in-scope only for invite recipients. Not changed in this pass.
- The repo description change is visible immediately on GitHub; no other system consumes it.

## Risks and Mitigations

- **Risk:** Rewriting the README loses subtle accuracy about what works vs. what doesn't (CDP drop rate, eBay fingerprint rejection, Linux/Windows non-support). Mitigation: keep the Status section honest. Don't make claims in the lede that the Status section then contradicts.
- **Risk:** The framing "your agent is logged in" is true for sidecar-aware agents and adapter-served agents but partial for browser-automation agents reading Chrome's SQLite on the sink (the 55% CDP drop rate). Mitigation: do not make Chrome-on-sink the headline; let it be one of several delivery surfaces in the "how it works" section, and let the Status section carry the honest caveats.
- **Risk:** Dropping the beta banner could mislead a future public-repo reader. Mitigation: the repo is currently private, so there is no public reader. Re-evaluate the banner question before the repo goes public.

## Acceptance Criteria

- `README.md` opens with an agent-outcome lede, not a technical claim.
- No mention of "beta" in `README.md`.
- No en-dashes, em-dashes, or markdown bold in `README.md`.
- No reference to any specific competitor product or named external skill in `README.md` or the GitHub repo description.
- The two corruptions in `README.md:41` and `docs/quickstart.md:125` are fixed.
- The repo description on GitHub matches the new framing.
- All in-repo links in the new README resolve to existing files.
