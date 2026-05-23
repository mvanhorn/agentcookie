---
title: "feat: secrets bus adoption standard (v2)"
type: feat
status: active
created: 2026-05-22
plan_depth: deep
related_specs:
  - docs/spec-agentcookie-secrets-bus-v1.md
related_runbooks:
  - docs/runbook-secrets-bus-adoption.md
  - docs/runbook-secrets-bus-gh-example.md
related_audits:
  - docs/audits/2026-05-22-pp-cli-auth-inventory.md
---

# feat: secrets bus adoption standard (v2)

## Problem Frame

The v0.13.0-beta.1 secrets bus shipped today is a working transport but a manual product. Onboarding a project means a human types `agentcookie secret import-from <path> --as <name>` once per CLI. That tax falls on the agentcookie user, not the project author, which is the wrong direction for adoption.

The right model is the opposite: a project author declares "I want to participate in agentcookie sync" by dropping a small manifest in their own repo. When a user installs that project, agentcookie discovers the manifest, registers the project, and ships the secrets on the next sync. Zero steps for the user, one file for the author.

This is the adoption standard. It lives on top of the v1 wire format; the format itself does not change.

## Scope

This plan covers:

- A new adoption manifest format that project authors ship inside their own repo (or install location).
- An agent-side discovery loop that finds those manifests on the source machine.
- A read-in-place mode so projects do not have to mirror their secrets into `~/.agentcookie/secrets/` manually.
- Backward compatibility with the v1 imperative `agentcookie secret import-from` flow.
- Three integration tiers - PP CLIs (auto-detect from existing `.printing-press.json`), skills/scripts (ship new manifest), arbitrary projects (drop manifest in known path).
- Prior-art-grounded design decisions, an `agentcookie discover` debug command, and an `agentcookie secret revoke` command for one-step removal.

### Out of scope

- Changes to the v1 wire envelope or the source-to-sink protocol.
- Sink-side discovery (sink remains a sink; only the source machine discovers project manifests).
- A central registry server. The standard is filesystem-based and trustless by default.
- Tesla-style signed-keypair file material. Those stay outside the bus per spec v1 section 1.2.
- The Python reader (`pkg/python/agentcookie_secret`) - already deferred to v0.13.1 in the v1 plan.

### Deferred to Follow-Up Work

- A sink-side companion runbook for receiving auto-discovered secrets (the sink already handles them; only the user-facing doc is deferred).
- Per-project signature verification (`signed_by` field). Reserve the schema slot now, ship the verification flow when there is a first user who needs it.
- A web-based directory of projects that have adopted the standard. Maybe useful later; not core.

---

## Requirements Trace

| R-ID | Requirement | Origin |
|------|-------------|--------|
| R1 | Project authors declare adoption via a single file in their repo or install location | User request: "if last30days sets up cookiesync connector properly, it would auto be picked up" |
| R2 | Agentcookie on the source machine discovers declarations automatically at startup and on filesystem events | User request: "automagically if last30days embedded it" |
| R3 | Three integration tiers, each with a documented adoption path | Inferred from research: PP CLIs already have `.printing-press.json`, last30days is a skill not a CLI, anyone else needs a generic path |
| R4 | The v0.13 wire format and reader library remain unchanged; this is additive | Scope decision |
| R5 | Manifest schema is inert data (TOML), no code execution at discovery time | Web-research learning: Chrome MV3 permission sprawl + direnv `allow` gate both emphasize trust-by-restraint |
| R6 | Path traversal in declared names is blocked at discovery time, not at import time | Learnings: filepath-join-traversal-with-user-input-2026-03-29 |
| R7 | When a declaration and the agent both have a candidate identity field, the declaration wins | Learnings: manifest-wins-over-re-derivation-2026-05-12 |
| R8 | Soft validation (warn + skip) at discovery time, hard validation at explicit import time | Learnings: soft-validation-in-reusable-library-packages-2026-05-06 |
| R9 | Two collision-handling rules: explicit `name` field collision is an error, derived collision is suffix-stable | Learnings: identifier-collision-uniquification-pattern-2026-05-08 |
| R10 | Read-in-place by default, copy-to-bus as fallback for projects whose .env path is too dynamic | User: "would take your primary computer and move it over" - implies the source-of-truth stays in the project, not in the bus |
| R11 | A standard file name and well-known-path convention so users can grep their machine for adopted projects | Web research: editorconfig + Infisical `.infisical.json` both used filename-as-identity |
| R12 | A debug command (`agentcookie discover`) listing what was found, what was rejected, and why | Inferred from spec_flow analysis - opaque discovery is the failure mode of every standard I researched |
| R13 | A removal command (`agentcookie secret revoke <name>`) that undoes registration without nuking the project | Symmetry with R1 - if adoption is one-file, removal should be one-command |

---

## High-Level Technical Design

This illustrates the intended approach. It is directional guidance for review, not implementation specification.

### Discovery model

```
Source machine startup:
   1. Scan well-known paths (in priority order):
      a. ~/.agentcookie/manifests/        # user-installed declarations
      b. ~/.config/agentcookie/manifests/ # XDG-style alt
      c. /usr/local/share/agentcookie/manifests/  # system-installed
      d. ~/printing-press/library/*/.printing-press.json (read auth_env_vars)
      e. Any path the user added via `agentcookie discover --add-path`

   2. For each candidate manifest:
      a. Validate schema; soft-skip if invalid (log to stderr)
      b. Validate the declared name (lowercase-hyphen-only, no traversal)
      c. Collision check against already-known names
      d. Register the project in an in-memory map: name -> {source_kind, source_path, read_in_place_path, sync_keys}

   3. fsnotify watch on each well-known directory: new manifests get picked up live.

   4. On every source --watch event AND every source --once:
      a. Walk the in-memory registry
      b. For each registered project with read_in_place_path: read that file fresh
      c. Apply the project's manifest sync policy (key allowlist/blocklist)
      d. Merge into the existing ~/.agentcookie/secrets/<name>/ payload OR ship straight from the in-memory read
      e. Send via the existing v0.13 wire envelope - sink does not change
```

### Manifest schema (preview)

```toml
# Generic agentcookie.toml - applies to any project type.
schema_version = 1
name = "last30days"                           # slug, lowercase-hyphen-only
display_name = "last30days"                   # human label
description = "Brand intelligence skill"      # one-line; goes in `agentcookie discover` output
project_kind = "skill"                        # "cli" | "skill" | "service" | "other"
homepage = "https://github.com/mvanhorn/last30days-skill"

# Where the secrets live on the user's machine. One of:
[secrets.file]                                # read this .env file in place
path = "~/.config/last30days/.env"

# OR:
[secrets.command]                             # run this command, parse env-shaped output
exec = ["last30days", "dump-secrets"]         # rare, advanced

# OR:
[secrets.keychain]                            # macOS keychain lookup (planned, deferred to v2.1)
service = "last30days"

# Filter what gets shipped. Same shape as the v1 manifest's [sync] table.
[sync]
default = true

[sync.keys]
SETUP_COMPLETE = false                        # local config, not a secret
FROM_BROWSER = false
```

### Decision matrix: which integration tier applies

| Project type | Tier | Manifest file | Discovery path |
|--------------|------|---------------|----------------|
| PP CLI | A (zero-touch) | Existing `.printing-press.json` | `~/printing-press/library/*/.printing-press.json` |
| Skill or scripted tool | B (drop manifest in repo) | `agentcookie.toml` | Project author's choice of install location, declared once |
| Arbitrary CLI / native app | C (drop manifest in known path) | `~/.agentcookie/manifests/<name>.toml` | User or installer drops it |
| Already manually imported via v1 flow | Legacy | `~/.agentcookie/secrets/<name>/secrets.env` | Unchanged; v1 path always works |

The three tiers are not mutually exclusive. A PP CLI may also ship `agentcookie.toml` to override behavior; precedence: explicit manifest > derived from `.printing-press.json`.

---

## Output Structure

```
agentcookie/
├── docs/
│   ├── spec-agentcookie-secrets-bus-v2-adoption.md       # the new spec
│   ├── runbook-adoption-manifest-author.md               # how to ship a manifest from your project
│   ├── runbook-adoption-pp-cli.md                        # PP-CLI-specific path (mostly automatic)
│   └── runbook-adoption-skill.md                         # last30days-style
├── internal/
│   └── secretsbus/
│       ├── discovery.go              # NEW: the discovery loop
│       ├── discovery_test.go         # NEW
│       ├── manifest_v2.go            # NEW: the v2 adoption manifest parser
│       ├── manifest_v2_test.go       # NEW
│       ├── pp_cli_adapter.go         # NEW: read .printing-press.json -> v2 manifest in memory
│       ├── pp_cli_adapter_test.go    # NEW
│       └── (existing files unchanged)
├── internal/cli/
│   ├── discover.go                   # NEW: `agentcookie discover` subcommand
│   ├── discover_test.go              # NEW
│   ├── secret_revoke.go              # NEW: `agentcookie secret revoke`
│   └── secret_revoke_test.go         # NEW
├── pkg/
│   └── agentcookieadoption/          # NEW: thin public Go API for authors who want to embed
│       ├── adoption.go               #   helpers to write/validate a manifest from the project side
│       └── adoption_test.go
└── examples/
    ├── adoption-last30days/          # NEW
    │   ├── agentcookie.toml
    │   └── README.md
    └── adoption-third-party-cli/     # NEW
        ├── agentcookie.toml
        └── README.md
```

---

## Key Technical Decisions

### Decision 1: Manifest filename is `agentcookie.toml`, not `.cookiesync` or `.agentcookie`

- **Why** - The hidden-dotfile convention (`.editorconfig`, `.envrc`, `.infisical.json`) trades discoverability for unobtrusiveness. Adoption standards win when authors can grep for the file from their own repo without `ls -la`. `agentcookie.toml` is visible-by-default in repo listings, GitHub UIs, and IDE file trees. The user gets unobtrusiveness from the `~/.agentcookie/manifests/` location, not from the filename.
- **Alternatives considered**: `.cookiesync` (rejected - name was withdrawn during scoping); `.agentcookie` (rejected - poor IDE visibility); `cookiesync.json` (rejected - JSON has no comments, schema docs hurt); `agentcookie.yaml` (rejected - YAML quoting traps, no spec precedent in this repo).

### Decision 2: Filesystem convention, not central registry

- **Why** - Prior-art research showed 1Password Shell Plugins' PR-gated adoption is the largest single drag on their plugin count. Compiled-in registries scale linearly with maintainer attention; filesystem conventions scale infinitely.
- **Alternatives considered**: GitHub-issue-based registration (rejected - high friction); central server (rejected - introduces dependency); compiled-in (rejected - 1Password lesson).

### Decision 3: Walk-up discovery is rejected in favor of well-known paths

- **Why** - direnv's walk-up model works because `direnv` is invoked per-shell-cd. Agentcookie source is a daemon; walk-up has no natural anchor. Well-known paths (`~/.agentcookie/manifests/`, `~/printing-press/library/*/.printing-press.json`) are deterministic and watchable.
- **Alternatives considered**: cwd walk-up like Infisical (rejected - daemon mismatch); home-dir-wide find (rejected - performance + privacy: `find ~/` for `agentcookie.toml` is invasive).

### Decision 4: Read-in-place by default; copy-to-bus is the v1 legacy path

- **Why** - User explicitly wants the project's existing `.env` to remain source of truth. Mirroring values doubles the surface; read-in-place means rotating a token in the project file ships to sink on the next sync with no extra step. Copy-to-bus stays available for projects whose `.env` path is dynamic or computed.
- **Alternatives considered**: copy-only (rejected - drift); symlink (rejected - macOS LaunchAgent + sealed twin interactions get weird); read-in-place only (rejected - some projects do not have a stable file).

### Decision 5: PP CLIs are first-class adopters via printing-press generator, with `.printing-press.json` auto-detect as fallback

- **Why** - PP CLIs and agentcookie are sibling tools in the same ecosystem; making them visibly cooperate is a product feature, not just plumbing. The printing-press generator emits two new artifacts per CLI: an explicit `agentcookie.toml` (declarative coupling) and a generated Go file that calls `pkg/agentcookiesecret.Load(<cli-name>)` at startup and merges bus values into the existing auth-load path (functional coupling). The `.printing-press.json` auto-detect adapter (U3) remains as the fallback for older PP CLIs that have not yet regenerated.
- **Alternatives considered**: Auto-detect only (rejected per user direction - PP CLIs and agentcookie should be strongly connected, not loosely coupled); require hand-authored manifests per CLI (rejected - 30+ PP CLIs is too much manual work and drifts as new ones are added).
- **Implication:** This plan now spans two repos (`mvanhorn/agentcookie` for the standard, `mvanhorn/cli-printing-press` for the generator change). U12 and U13 cover the printing-press side.

### Decision 6: Discovery happens on the source machine only

- **Why** - The sink already accepts whatever the source ships via the v1 envelope. Sink-side discovery would mean every friend re-discovers projects from their own filesystem, which is exactly backwards: the friend should receive what you authorized to send, not what they themselves have installed.
- **Alternatives considered**: Symmetric discovery (rejected - confuses the trust model); sink-side filter (rejected - source-side filter already does this via `[sync.keys]`).

### Decision 7: Soft validation at discovery, hard validation at explicit import

- **Why** - Learnings #4. Hard-failing the discovery loop on one malformed manifest punishes every other project on the user's machine. Soft-skip with stderr warning maintains forward progress. The explicit `agentcookie secret import-from` path is the user's "I really mean it" gate and stays hard-fail.

### Decision 8: Identity-collision behavior is split by source

- **Why** - Learnings #3. Two cases:
  - **Explicit collision** (two `agentcookie.toml` files declare the same `name`): hard error. The user is being lied to and should know.
  - **Derived collision** (PP CLI auto-derivation produces the same name as an explicit manifest): explicit wins, derived is suffixed `<name>-pp` and logged.

### Decision 9: Manifest schema reserves `signed_by` but does not implement signature verification in v2

- **Why** - A v2.1 concern. Reserving the field now means a future signed manifest can use the same parser. Premature implementation would block v2 on PKI tooling that has no user yet.

---

## Scope Boundaries

### Deferred for later

- Signature verification (`signed_by` field reserved, no parser yet).
- macOS Keychain as a `[secrets.keychain]` source. Spec the schema; implement when there is a first concrete user.
- A web-based directory listing of projects that have adopted the standard.

### Deferred to Follow-Up Work

- Sink-side runbook describing what users see when a new project's secrets arrive.
- Telemetry on which integration tier is most-used. Useful for v3 prioritization; not blocking v2.
- A `printing-press` plugin that emits `agentcookie.toml` alongside `.printing-press.json` for the cases where the auto-derivation is wrong.

### Non-goals

- Becoming a general-purpose secret-manager. Vault, Doppler, Infisical, 1Password all do this professionally. The bus is for peer-to-peer source-to-sink delivery only.
- Cross-machine discovery. Each machine's source discovers from its own filesystem only.
- Replacing `agentcookie secret import-from`. That path stays; it is the right interface for one-off manual cases.

---

## Implementation Units

### U1. Adoption-standard specification document

**Goal:** Write the canonical spec for the v2 adoption manifest. Mirrors the shape of `docs/spec-agentcookie-secrets-bus-v1.md` (which covers the wire format) and is explicitly additive to it.

**Requirements:** R1, R5, R11, R13

**Dependencies:** None

**Files:**
- `docs/spec-agentcookie-secrets-bus-v2-adoption.md` (create)

**Approach:**
- Section 1: scope and non-scope (this is about adoption, not the wire format).
- Section 2: the `agentcookie.toml` schema (required fields, optional fields, reserved fields including `signed_by`).
- Section 3: well-known discovery paths in priority order.
- Section 4: integration tiers A/B/C with concrete examples for each.
- Section 5: name validation rules, collision rules, traversal protection.
- Section 6: precedence (explicit manifest > PP CLI auto-derivation > legacy v1 bus directory).
- Section 7: relationship to v1 - read-in-place vs copy-to-bus, when each fires.
- Section 8: forward-compatibility commitments (schema versioning, reserved fields).
- Section 9: governance - where the spec lives, how it evolves, who decides.

**Patterns to follow:** `docs/spec-agentcookie-secrets-bus-v1.md` for shape, tone, numbered subsections, and the explicit Valid/Invalid example pattern.

**Test scenarios:** none -- pure documentation

**Verification:** The spec is detailed enough that an independent implementer could write a compatible discovery loop in another language from it alone, without reading the agentcookie Go code.

---

### U2. Manifest v2 parser

**Goal:** Parse the `agentcookie.toml` schema into a strongly typed Go struct with full validation.

**Requirements:** R5, R6, R7, R8

**Dependencies:** U1 (spec must exist first)

**Files:**
- `internal/secretsbus/manifest_v2.go` (create)
- `internal/secretsbus/manifest_v2_test.go` (create)

**Approach:**
- New `ManifestV2` type, distinct from the existing `Manifest` (which is the v1 sync-override TOML inside `~/.agentcookie/secrets/<name>/manifest.toml`).
- Use existing `github.com/BurntSushi/toml` dep.
- `ParseManifestV2(path string) (*ManifestV2, error)` returns the typed struct plus a slice of soft-warnings (unknown fields, deprecated fields).
- Reuse the v1 `validCLIName` helper from `secretsbus.go` for `name` validation; add `validDisplayName` for the display field.
- Reject `..` in path segments; reject absolute paths in `[secrets.file].path` unless they start with `~/` (which is then expanded relative to the current user's home).

**Patterns to follow:**
- `internal/secretsbus/secretsbus.go` `loadManifest()` for two-pass TOML decode pattern (needed if we want to detect explicit-vs-omitted sync.default).
- The "Manifest" type's `defaultSync()` pattern for handling absent fields.

**Test scenarios:**
- happy path: full manifest with all sections parses; all fields land on the struct.
- happy path: minimal manifest with only `schema_version`, `name`, and `[secrets.file]` parses.
- edge case: unknown top-level field surfaces a soft-warning but does not fail parse.
- edge case: `schema_version = 2` is the only accepted value; 1 is rejected with a specific message pointing to v1 location.
- edge case: `[secrets.file]`, `[secrets.command]`, and `[secrets.keychain]` are mutually exclusive; declaring more than one is a hard error.
- error path: `name = "../etc"` is rejected at parse time.
- error path: `name = "Foo"` (uppercase) rejected at parse time.
- error path: `[secrets.file].path` containing `..` rejected.
- error path: `[secrets.keychain]` declared in v2 returns a "not yet implemented" error (slot reserved, parser knows about it).
- error path: completely malformed TOML returns the bare toml error wrapped with the path that failed.

**Verification:** All test scenarios pass; the parser refuses every form of escape we documented in section 5 of the spec.

---

### U3. PP CLI adapter

**Goal:** Synthesize an in-memory v2 manifest from a `.printing-press.json` file so PP CLIs get adoption for free.

**Requirements:** R3, R4 (tier A)

**Dependencies:** U2

**Files:**
- `internal/secretsbus/pp_cli_adapter.go` (create)
- `internal/secretsbus/pp_cli_adapter_test.go` (create)

**Approach:**
- `DeriveManifestFromPP(jsonPath string) (*ManifestV2, error)`.
- Map `.printing-press.json` fields to v2:
  - `cli_name` -> `name`
  - `display_name` -> `display_name`
  - `description` -> `description`
  - `auth_env_vars` -> the list of keys the synthesized `[sync]` block should ship by default.
  - `auth_env_var_specs[].sensitive == true` -> include in default-sync set; non-sensitive -> sync.keys.X = false.
- Derive the on-disk `.env` path from PP CLI convention: `~/.config/<cli_name>/config.toml` (the U1 audit confirmed this is canonical for 11+ CLIs).
- Adapter never reads the actual secrets; it only produces a manifest pointing at where they live.

**Patterns to follow:**
- Audit findings in `docs/audits/2026-05-22-pp-cli-auth-inventory.md` for the canonical PP CLI auth shapes.

**Test scenarios:**
- happy path: tesla `.printing-press.json` produces a manifest with `name = "tesla-pp-cli"`, `[secrets.file].path = "~/.config/tesla-pp-cli/config.toml"`, sync includes `TESLA_AUTH_TOKEN`.
- edge case: PP CLI with no `auth_env_vars` produces a manifest with `[secrets.file]` set but empty sync allowlist (logs that this CLI has no documented secrets).
- edge case: `auth_env_var_specs` says `sensitive = false` -> the key is excluded from default-sync.
- error path: malformed `.printing-press.json` -> wrapped error referencing the file path.
- error path: missing `cli_name` field -> hard error (PP CLI metadata invariant violated).
- integration: derived manifest passes the v2 validator from U2.

**Verification:** Running the adapter against every `.printing-press.json` in `~/printing-press/library/*` produces valid manifests for all of them; no parser panics.

---

### U4. Discovery loop

**Goal:** Build the source-side discovery engine that walks well-known paths, parses every manifest it finds, applies collision rules, and produces a deduplicated registry of projects.

**Requirements:** R2, R3, R7, R8, R9, R11

**Dependencies:** U2, U3

**Files:**
- `internal/secretsbus/discovery.go` (create)
- `internal/secretsbus/discovery_test.go` (create)

**Approach:**
- `Registry` type holds `map[string]RegisteredProject` keyed by slug.
- `RegisteredProject` carries source kind (`explicit-manifest` | `pp-cli-derived` | `legacy-v1`), source path, the parsed manifest, and `read_in_place_path`.
- `Discover(homeDir string) (*Registry, []error)` walks paths in priority order:
  1. `~/.agentcookie/manifests/*.toml`
  2. `~/.config/agentcookie/manifests/*.toml`
  3. `/usr/local/share/agentcookie/manifests/*.toml`
  4. `~/printing-press/library/*/.printing-press.json`
  5. Legacy: existing entries in `~/.agentcookie/secrets/<name>/` get a synthetic registered project pointing at themselves.
- Collision handling:
  - Two explicit manifests with the same `name` -> hard error, both rejected, stderr message names both source paths.
  - One explicit + one derived (PP) collision -> explicit wins; derived gets `<name>-pp` suffix and a logged note.
  - Two derived collisions (unlikely) -> first one wins by path-sort order; second is suffixed with sha256-prefix-6.
- Soft-skip rule: any single manifest parse error logs to stderr but does not abort the loop.

**Patterns to follow:**
- `internal/secretsbus/secretsbus.go` `LoadPayload` for the multi-error return pattern.
- `internal/secretsbus/watcher.go` for fsnotify wiring (the watcher in U5 reuses this engine).

**Test scenarios:**
- happy path: discovery on a clean home dir with one `~/.agentcookie/manifests/last30days.toml` produces a registry with one entry, source kind `explicit-manifest`.
- happy path: discovery with both an explicit manifest AND a PP CLI metadata file produces two entries, no collision.
- happy path: legacy v1 bus directory `~/.agentcookie/secrets/foo/secrets.env` produces a synthetic registry entry with source kind `legacy-v1`.
- edge case: empty home dir produces an empty registry with no errors.
- edge case: same project declared in `~/.agentcookie/manifests/` AND `~/.config/agentcookie/manifests/` -> the higher-priority path wins, lower path is soft-skipped with a log.
- edge case: PP CLI auto-derived name `tesla-pp-cli` collides with an explicit manifest named `tesla-pp-cli` -> explicit wins, derived is suffixed to `tesla-pp-cli-pp` and logged.
- error path: two explicit manifests claim `name = "demo"` -> both are rejected, registry does not include either, both source paths appear in the error.
- error path: a manifest is unreadable due to permissions -> single skip, other manifests still discovered.
- integration: running discovery, then `secretsbus.LoadPayload`, then the discovery's read-in-place paths, produces a unified payload that includes both legacy and v2 entries.

**Verification:** All test scenarios pass. Discovery is idempotent: running it twice on the same filesystem state returns identical registries.

---

### U5. Discovery watcher

**Goal:** fsnotify-driven live updates so that adding a new manifest file (or installing a new PP CLI) triggers discovery without restarting agentcookie.

**Requirements:** R2

**Dependencies:** U4

**Files:**
- `internal/secretsbus/discovery.go` (extend)
- `internal/secretsbus/discovery_test.go` (extend)

**Approach:**
- New `DiscoveryWatcher` modeled on the existing `Watcher` in `internal/secretsbus/watcher.go`.
- Watch the three manifest directories plus `~/printing-press/library/`.
- On Create/Write/Rename in any watched path, re-run `Discover()` and diff against the previous registry.
- On diff, emit a `RegistryDelta` (added, removed, changed) so the source push pipeline (U6) can act on it.
- Debounce identical to the v1 watcher (default 250ms).

**Patterns to follow:** `internal/secretsbus/watcher.go` end-to-end; this is a parallel pattern.

**Test scenarios:**
- happy path: dropping a new `~/.agentcookie/manifests/foo.toml` while the watcher is running fires onChange exactly once after debounce.
- happy path: installing a new PP CLI (touching its `.printing-press.json`) fires onChange.
- edge case: rapid sequence of writes to the same manifest fires onChange once per debounce window.
- edge case: watcher started when `~/.agentcookie/manifests/` does not exist polls for the directory to appear (matches v1 `waitForRoot` semantics).
- error path: a manifest deleted at runtime appears as `removed` in the delta.
- integration: full restart of the watcher across a manifest rename produces a delta with one removed + one added entry.

**Verification:** Watcher behaves identically in shape to the v1 watcher; reusing the same patterns means the test-coverage approach is the same.

---

### U6. Source push pipeline integration

**Goal:** Wire the discovery registry into the source-side push so that every push (both `--once` and `--watch` fs events) consults the registry, reads each project's secrets in place, and includes them in the wire envelope alongside the v1 bus payload.

**Requirements:** R2, R4, R10

**Dependencies:** U4, U5

**Files:**
- `internal/cli/source.go` (modify)
- `internal/cli/source_test.go` (extend)

**Approach:**
- At source startup, run discovery once; cache the registry.
- In `--watch` mode, also start the discovery watcher; on each delta, refresh the cache.
- In `pushOnce`:
  1. Load v1 bus payload as today (`secretsbus.LoadPayload(home)`).
  2. For each registered project where `secrets.file.path` is set: read that file fresh, apply the project's `[sync.keys]` filter, merge into the in-flight payload under `<project.name>`.
  3. Read-in-place collisions with v1 bus entries: v1 bus wins (it is the user's explicit per-key state).
  4. Hand the merged payload to the existing push code.
- No envelope changes; the v1 `Secrets map[string]map[string]string` field already carries everything.

**Patterns to follow:** `internal/cli/source.go` existing `pushOnce` shape.

**Test scenarios:**
- happy path: source --once with one v1 bus entry AND one discovered project produces an envelope containing both, keyed by name.
- happy path: discovered project's `[sync.keys] FOO = false` excludes FOO from the envelope.
- edge case: discovered project's file does not exist at read time -> entry is omitted, stderr warns once per push (not per file scan).
- edge case: v1 bus has `name = "foo"` AND discovery finds an explicit manifest with `name = "foo"` -> v1 bus values win for keys that are in both; discovered-only keys are still included.
- edge case: discovered project read returns 0 keys -> entry is omitted from envelope (no empty CLI dirs created on sink).
- integration: full push through to a local test sink confirms the sink-side `WritePayload` accepts the merged payload without modification.

**Verification:** End-to-end test: drop an `agentcookie.toml` in a temp home, run source --once, assert the sink received the expected secrets without any v1 bus manipulation.

---

### U7. `agentcookie discover` subcommand

**Goal:** A debug command that lists everything the discovery engine found, what it rejected, and why. The user's window into the otherwise-invisible auto-detection.

**Requirements:** R12

**Dependencies:** U4

**Files:**
- `internal/cli/discover.go` (create)
- `internal/cli/discover_test.go` (create)
- `internal/cli/root.go` (modify - register subcommand)

**Approach:**
- `agentcookie discover` runs `Discover()` and prints a table:
  - Project name | tier (explicit / pp-derived / legacy-v1) | source path | read-in-place path | key count | sync filter summary
- `--json` flag for machine-readable output.
- `--verbose` includes skipped paths and the reason for each skip.
- `--add-path <dir>` writes a new watched path to the agentcookie config and exits (next discovery run picks it up).
- `--remove-path <dir>` inverse.

**Patterns to follow:** `internal/cli/doctor.go` for the multi-row table output + `--json` flag handling.

**Test scenarios:**
- happy path: a home dir with three projects produces a three-row table.
- happy path: `--json` produces parseable JSON with the same field set as the table.
- happy path: `--verbose` includes the explicit "skipped because parse error at line N" reasons.
- edge case: empty home dir prints a single line "no adopted projects found" and exit 0.
- edge case: `--add-path` then a normal `discover` invocation shows the new path was scanned.
- error path: `--add-path /nonexistent` -> the path is recorded but discovery soft-skips it; next `discover` shows the skip reason.

**Verification:** A user can run `agentcookie discover` and reconstruct the registry by hand from the output.

---

### U8. `agentcookie secret revoke` subcommand

**Goal:** One-step removal of a project's adoption. Symmetric with the one-file adoption.

**Requirements:** R13

**Dependencies:** U4, U6

**Files:**
- `internal/cli/secret_revoke.go` (create)
- `internal/cli/secret_revoke_test.go` (create)
- `internal/cli/secret.go` (modify - register subcommand under `secret`)

**Approach:**
- `agentcookie secret revoke <name>`:
  1. Look up `<name>` in the current registry.
  2. If tier is `explicit-manifest`: ask before deleting the manifest file (or with `--force`).
  3. If tier is `pp-cli-derived`: write a synthetic exclusion to `~/.agentcookie/manifests/_excluded.toml` so the derivation is silenced going forward.
  4. If tier is `legacy-v1`: remove the directory under `~/.agentcookie/secrets/<name>/` (this is the existing `agentcookie secret rm <name>` behavior; revoke wraps it).
- `--all-tiers` revokes across every tier where `<name>` appears.

**Patterns to follow:** `internal/cli/secret.go` for the cobra subcommand structure and confirmation-flag style.

**Test scenarios:**
- happy path: revoke an explicit-manifest project deletes the manifest file (with confirmation).
- happy path: revoke a PP-derived project writes the exclusion entry and the next discovery skips it.
- happy path: revoke a legacy-v1 project removes `~/.agentcookie/secrets/<name>/`.
- edge case: revoke a name that exists in two tiers without `--all-tiers` -> prompt asks which one; with `--all-tiers` -> all are revoked.
- error path: revoke a name that does not exist -> exit 1 with "no such adopted project: <name>" and a suggestion to run `discover`.
- integration: revoke then push -> the next envelope does not include the revoked project's secrets.

**Verification:** Round-trip: adopt -> push -> revoke -> push, confirms the sink receives the project's secrets in the first push and does not in the second.

---

### U9. Public adoption helper package

**Goal:** A thin Go package that project authors can import to programmatically write or validate an `agentcookie.toml`. Not required for adoption (you can hand-write the file); useful for tooling, generators, and the PP CLI's own emission path.

**Requirements:** R1

**Dependencies:** U2

**Files:**
- `pkg/agentcookieadoption/adoption.go` (create)
- `pkg/agentcookieadoption/adoption_test.go` (create)
- `pkg/agentcookieadoption/doc.go` (create - godoc package comment)

**Approach:**
- `Manifest` struct re-export from `internal/secretsbus/manifest_v2.go`'s `ManifestV2`.
- `Validate(m *Manifest) error` runs the same validators as the parser but on an already-constructed struct.
- `WriteTo(m *Manifest, path string) error` emits a canonical TOML representation (sorted keys, the spec's preferred ordering, header comment pointing to the spec).
- No discovery or push logic - this package is for authors, not the agent itself.

**Patterns to follow:** `pkg/agentcookiesecret/load.go` for the public package shape, doc.go pattern, and zero-non-stdlib-deps-beyond-keystore discipline (here: zero-non-stdlib-deps-beyond-burntsushi-toml).

**Test scenarios:**
- happy path: construct a `Manifest` in code, `Validate`, `WriteTo` produces a file that the v2 parser reads back to the same value.
- edge case: `WriteTo` of a manifest with a `[sync.keys]` block produces stable ordering across runs (sorted keys).
- error path: `Validate` on a manifest with `name = "Foo"` returns the same error message the parser would.
- integration: write -> parse round-trip preserves all fields.

**Verification:** Independent project (e.g., a hypothetical printing-press plugin) could import this package and emit a valid manifest for any of its CLIs.

---

### U10. Documentation and worked examples

**Goal:** Three runbooks for the three integration tiers, plus two `examples/` directories that work as drop-in references.

**Requirements:** R1, R3

**Dependencies:** U1 (spec), U7 (`discover` for the validation step in each runbook)

**Files:**
- `docs/runbook-adoption-pp-cli.md` (create)
- `docs/runbook-adoption-skill.md` (create)
- `docs/runbook-adoption-manifest-author.md` (create - the generic third-party-CLI runbook)
- `examples/adoption-last30days/agentcookie.toml` (create)
- `examples/adoption-last30days/README.md` (create)
- `examples/adoption-third-party-cli/agentcookie.toml` (create)
- `examples/adoption-third-party-cli/README.md` (create)

**Approach:**
- The PP CLI runbook is the shortest: "you do nothing; verify with `agentcookie discover`." Document the 1-line override case.
- The skill runbook walks through last30days specifically: where to put `agentcookie.toml`, which `[sync.keys]` exclusions make sense for that project (`SETUP_COMPLETE`, `FROM_BROWSER` are not secrets), and how to handle the install-time copy from the repo into the user's `~/.agentcookie/manifests/` if applicable.
- The generic manifest-author runbook walks through `examples/adoption-third-party-cli/` end-to-end.
- Each runbook ends with a "verify with `agentcookie discover`" step.

**Patterns to follow:** `docs/runbook-secrets-bus-adoption.md` for runbook shape (the v1 author-side runbook).

**Test scenarios:** none -- documentation only.

**Verification:** A volunteer who reads only the skill runbook can ship `agentcookie.toml` for a new skill of their own and see it appear in `agentcookie discover` output on their machine.

---

### U12. Printing-press generator: emit `agentcookie.toml` per CLI

**Target repo:** `mvanhorn/cli-printing-press` (NOT this repo)

**Goal:** Modify the printing-press generator so every CLI it produces ships an `agentcookie.toml` in its repo root, derived from the existing `.printing-press.json` metadata. Adopting a new PP CLI means typing `pp generate <api>`; the generator now silently adds bus participation.

**Requirements:** R1, R3 (tier A made strong instead of free-rider)

**Dependencies:** U1 (spec must be locked first), U2 (parser stable so generated files are guaranteed-valid)

**Files (in cli-printing-press repo):**
- `internal/generator/templates/agentcookie.toml.tmpl` (create) - the Go-text/template for the file
- `internal/generator/agentcookie_emit.go` (create) - the emit step that consumes the template
- `internal/generator/generator.go` (modify) - register the new emit step alongside the existing `.printing-press.json` emit
- `internal/generator/agentcookie_emit_test.go` (create)

**Approach:**
- Hook the emit at the same lifecycle stage as `.printing-press.json`: after spec normalization, before file write.
- Template inputs: cli_name, display_name, description, the canonical `~/.config/<cli_name>/config.toml` path, the `auth_env_var_specs` list filtered to `sensitive = true`.
- Emit a header comment in the generated TOML: `# Generated by printing-press <version> from <spec_path>. Do not edit; rerun generation to update.`
- Idempotency: if a hand-edited `agentcookie.toml` already exists with header marker `# agentcookie-manual-override`, skip emission for that CLI and log a warning. Authors who want to override the generated manifest do so by removing the auto-generated header.
- Include `agentcookie.toml` in `.printing-press-patches.json` tracking so re-runs preserve any manual additions per the cli-printing-press generator conventions.

**Patterns to follow:** Existing `internal/generator/printing_press_json_emit.go` (or whatever the analogous file is called - the agent should locate it during execution). Apply the [[manifest-wins-over-re-derivation-for-identity-fields]] convention: generated fields preserve any cli_name that was already chosen, not a re-derived one.

**Test scenarios:**
- happy path: generating tesla emits `~/printing-press/library/tesla/agentcookie.toml` with `name = "tesla-pp-cli"`, `[secrets.file].path = "~/.config/tesla-pp-cli/config.toml"`, and `[sync.keys]` set to include only sensitive env vars.
- happy path: regeneration produces byte-identical output (deterministic sort order).
- edge case: a CLI with no `auth_env_var_specs` emits a manifest with empty `[sync.keys]` and a TODO comment in the generated file (visible to the human reviewer).
- edge case: existing `agentcookie.toml` with the manual-override marker is preserved verbatim.
- error path: missing `cli_name` in spec -> hard error before any file is written.
- integration: the emitted file is parseable by the v2 parser from U2 (we will need either an integration test harness that pulls in agentcookie or a snapshot file checked into cli-printing-press for round-trip verification).

**Verification:** Running `pp generate tesla` against the existing tesla spec produces `agentcookie.toml` whose `discover`-output matches the U3 adapter's output for the same CLI. Generator and adapter agree.

---

### U13. PP CLI runtime: generated bus-reader integration

**Target repo:** `mvanhorn/cli-printing-press` (NOT this repo)

**Goal:** Generated PP CLI Go code calls `pkg/agentcookiesecret.Load(name)` at startup and merges bus-supplied values into the existing auth load path. PP CLI users get bus delivery without any code change in their generated CLI's main flow.

**Requirements:** R1, R3, R10

**Dependencies:** U12 (manifest emit), pkg/agentcookiesecret already shipped in v0.13

**Files (in cli-printing-press repo):**
- `internal/generator/templates/auth_load_with_bus.go.tmpl` (create) - the new auth-load shim template
- `internal/generator/auth_emit.go` (modify - or new) - emit step for the auth file
- `internal/generator/auth_emit_test.go` (create)
- The generated PP CLI's `internal/auth/load.go` (this is the file in *each generated CLI* - the template produces it) gets a new call site

**Approach:**
- The generated auth-load function adopts the pattern from `docs/runbook-secrets-bus-adoption.md` Pattern A: bus read first, file fallback second, env third.
- Generated file imports `github.com/mvanhorn/agentcookie/pkg/agentcookiesecret`.
- At the top of `Load()`:
  ```
  busEnv, busErr := agentcookiesecret.Load("<cli_name>")
  if busErr != nil && !errors.Is(busErr, agentcookiesecret.ErrInvalidCLIName) {
      // soft log, fall through
  }
  ```
- Each `auth_env_var_specs` entry generates an override block:
  ```
  if v := busEnv["<env_var_name>"]; v != "" { cfg.<FieldName> = v }
  ```
- Generated file carries a header comment naming the agentcookie version it was generated against, so regenerating with a newer agentcookie version produces a diff.
- Generator imports add `github.com/mvanhorn/agentcookie/pkg/agentcookiesecret` to the generated CLI's `go.mod`. The generator already manages `go.mod` for generated CLIs, so this is a one-line addition in the dependency emit step.
- Bus-wins-over-env at the merge: if the bus has the key, the bus value overrides whatever the existing file loader produced. This matches spec section 11.2.

**Patterns to follow:** Look at how the generator currently emits the auth-load code (there is an existing template producing `internal/auth/load.go`). Mirror its conventions exactly for field names, error handling, and log style.

**Test scenarios:**
- happy path: generating tesla produces an `internal/auth/load.go` that calls `agentcookiesecret.Load("tesla-pp-cli")` and assigns `TESLA_AUTH_TOKEN` from the bus when present.
- happy path: running the generated tesla binary against a populated bus reads the bus value over the file value.
- happy path: running the generated tesla binary against an empty bus falls through to the file value (no behavior change vs today).
- edge case: PP CLI with `auth_env_var_specs` containing multi-field credentials (e.g., client_id + client_secret) generates override blocks for all of them.
- edge case: PP CLI with `auth_type` other than `bearer_token` still gets the bus integration (the integration is field-by-field, not auth-type-aware).
- error path: invalid `cli_name` (this should never happen - the generator owns the name) -> generator hard-fails before emitting.
- integration: built tesla binary, populated `~/.agentcookie/secrets/tesla-pp-cli/secrets.env`, runs `tesla auth status` and sees the bus token's identity.

**Verification:** End-to-end: regenerate tesla-pp-cli, build, place a known token in the bus, run `tesla auth status`. The Source field of the auth status response (or its log line) should show the bus is being consulted.

---

### U14. PP CLI rollout: regenerate every shipped CLI

**Target repo:** `mvanhorn/cli-printing-press` (orchestration) + each PP CLI repo (the generated output)

**Goal:** Re-run the generator across all 30+ currently-shipped PP CLIs so they pick up the U12 manifest emission and U13 runtime integration. Open PRs to each PP CLI repo with the regenerated output.

**Requirements:** R3 (tier A across the board)

**Dependencies:** U12, U13

**Files:** Per-CLI; one PR per repo, all touching the same set of generated files (`agentcookie.toml`, `internal/auth/load.go`, `go.mod`).

**Approach:**
- Use the existing PP CLI roll-up script (or build one) to iterate across `~/printing-press/library/*/`, run `pp generate`, commit, and open a PR per CLI.
- Each PR's body links back to this plan and the v2 spec.
- CHANGELOG entry per CLI: "feat: integrate agentcookie secrets bus".
- Sequence: open PR for tesla-pp-cli first as the canary; once landed and verified end-to-end, roll out the rest in batches of 5 with same-day verification.
- Stop and ask the user before opening PRs against any external-collaborator PP CLI per memory rule [[no-courtesy-prs-external-forks]]. All current PP CLIs are user-owned, so this should not trigger.

**Patterns to follow:** Previous PP CLI batch rollouts. Memory rule [[evidence-on-every-pr]] applies - every PR includes an evidence block showing the generator output diff and at least one local verification screenshot or log excerpt.

**Test scenarios:** none -- this is rollout, not new code.

**Verification:** Every PP CLI repo has a merged PR adding `agentcookie.toml` and the bus-aware auth-load. `agentcookie discover` on a machine with the regenerated CLIs shows every one of them under tier A (`explicit-manifest` source, not `pp-cli-derived`).

---

### U11. End-to-end test and cut release

**Goal:** Live cross-machine validation that the entire adoption flow works for the two named target projects (last30days, a PP CLI), then cut a v0.14.0-beta.1 tag.

**Requirements:** All

**Dependencies:** U1 through U10, U12, U13. U14 may run after U11 ships (regenerate-and-roll-out can happen against a published v0.14).

**Files:**
- `CHANGELOG.md` (modify - in this repo)

**Approach:**
- Drop `agentcookie.toml` at `examples/adoption-last30days/agentcookie.toml` into `~/.agentcookie/manifests/last30days.toml` on the source machine.
- Run `agentcookie discover` -> verify last30days shows up, source kind `explicit-manifest`, read-in-place pointing at `~/.config/last30days/.env`.
- Verify the PP CLI tier with the canary regenerated CLI from U14 (tesla-pp-cli first): tesla-pp-cli shows up as `explicit-manifest` (its newly generated `agentcookie.toml`), AND running the rebuilt tesla binary reads from the bus.
- Verify the auto-detect fallback for non-regenerated PP CLIs: another PP CLI that has not yet been regenerated shows up as `pp-cli-derived`.
- Run `source --once`; on the sink, run `agentcookie secret list` and verify last30days and the PP CLI slugs appear with expected keys.
- Run `agentcookie secret revoke last30days`; re-run push; verify last30days is no longer on the sink.
- Write the CHANGELOG entry for v0.14.0-beta.1.
- PR to main; tag from main once merged.

**Patterns to follow:** v0.13's U11 end-to-end + release flow (the prior plan in this directory).

**Test scenarios:** none -- this is integration verification on real machines.

**Verification:** Sink-side `agentcookie secret list` shows the expected projects with the expected key sets at every step of the runbook. `agentcookie discover --json` output matches what was wired up.

---

## System-Wide Impact

This change affects:

- **Source machine startup** - new path scan adds ~10-50ms at startup; negligible for a daemon but worth knowing.
- **The wire envelope** - unchanged. Sinks running v0.13 accept v0.14 source pushes transparently.
- **Existing v1 bus users** - unchanged. Their `~/.agentcookie/secrets/<name>/` directories continue to work; the discovery loop treats them as synthetic `legacy-v1` registry entries.
- **PP CLIs** - first-class adopters. The printing-press generator emits `agentcookie.toml` and a bus-aware auth-load shim in every generated CLI (U12, U13). Existing CLIs get regenerated and shipped via PR (U14). Older un-regenerated PP CLIs still work via the `.printing-press.json` auto-detect adapter (U3) as a transparent fallback.
- **`mvanhorn/cli-printing-press` repo** - this plan spans two repos. The generator changes (U12, U13) and rollout (U14) land there. The spec, parser, discovery, and runtime live in this repo.
- **Sink machines** - no code change required. The sink already accepts arbitrary `<cli-name>` entries in the envelope.
- **Doctor** - the existing secrets-bus check should be extended to also report discovery state (count of adopted projects, count of skipped manifests). Adding to the U8 verification or U7 implementation; flagged in scope.

---

## Risk Analysis

- **Risk:** Auto-derived PP CLI entries leak secrets users did not realize were being shipped.
  **Mitigation:** The PP CLI adapter only ships keys that `.printing-press.json` marks `sensitive = true`. Non-sensitive auth-env-vars are excluded from default-sync. The `discover` output explicitly shows which keys will ship per project.

- **Risk:** Manifest schema drift between authors and the agent (author writes v1, agent expects v2).
  **Mitigation:** `schema_version` is required and validated; v1 is explicitly rejected with a pointer to the v1 spec location. Forward-compat: unknown top-level fields are warnings, not errors.

- **Risk:** Discovery loop slows down source startup significantly on a machine with many PP CLIs.
  **Mitigation:** Discovery is parallelizable per path; sort by mtime descending so the most-recent manifests come first; cap path-walk depth at 2.

- **Risk:** Two different machines (source and sink) end up with different registry contents.
  **Mitigation:** This is by design - sink does not run discovery. The source ships exactly what its registry says; the sink writes that. If the friend wants to see what was shipped, `agentcookie secret list` on the sink is the answer.

- **Risk:** A malicious project drops `agentcookie.toml` with `name = "tesla-pp-cli"` to shadow the PP CLI.
  **Mitigation:** Explicit-manifest wins over derived, but the discover output flags the override loudly. v2.1 can add a `signed_by` field for high-trust environments. For v2: rely on the fact that the user installed the manifest themselves; the bus does not auto-trust arbitrary network sources.

- **Risk:** The `[secrets.command]` exec source executes arbitrary user code at every push.
  **Mitigation:** Defer `[secrets.command]` to v2.1. Spec the schema; gate the parser to reject it at parse time for v2. The exec-source pattern is hostile to read-in-place semantics anyway (commands have side effects, files do not).

---

## Verification Strategy

- Unit tests on every new package per the per-unit test scenarios.
- Integration test that runs the full discovery -> push -> sink-write -> revoke -> push cycle in a temp HOME with a stubbed sink HTTP server. This is the closest we get to a real end-to-end without two machines.
- Manual cross-machine verification per U11 with the real source and sink.
- `agentcookie discover` output and `agentcookie secret list` on the sink together are the user-visible verification surface. Both ship in this plan; both have test coverage.

---

## Documentation Plan

- New spec: `docs/spec-agentcookie-secrets-bus-v2-adoption.md` (U1).
- Three new runbooks: PP CLI tier, skill tier, generic manifest-author tier (U10).
- Updated v1 runbook (`docs/runbook-secrets-bus-adoption.md`): add a section pointing at the new auto-adoption path as the recommended default; the imperative `agentcookie secret import-from` remains documented as the one-off escape hatch.
- CHANGELOG entry for v0.14.0-beta.1 (U11).
- New `examples/adoption-*` directories with working drop-in manifests.

---

## Alternatives Considered

### Walk-up discovery from cwd (Infisical / direnv style)

- **Considered because:** Lowest-friction author UX; the manifest lives in the project repo, no install step.
- **Rejected because:** Agentcookie source is a daemon launched from launchd. There is no cwd to walk up from. Walk-up fundamentally requires per-command invocation; that is not the bus's shape.

### PR-gated central registry (1Password Shell Plugins style)

- **Considered because:** Strong trust model; every adopter has been reviewed.
- **Rejected because:** 1Password's plugin count is bottlenecked by maintainer review. The bus is two-person infrastructure (a friend trusting another friend); the trust model is "you installed it" not "someone reviewed it."

### Single global manifest at `~/.agentcookie/projects.toml`

- **Considered because:** One file, easy to read and edit.
- **Rejected because:** Conflicts with the user-doesn't-touch-anything goal. The whole point of adoption is that the project's installer drops the manifest, not that the user maintains a registry by hand.

### XDG-only convention (`$XDG_DATA_DIRS`, `$XDG_CONFIG_DIRS`)

- **Considered because:** Mature, well-understood, cross-platform-ready.
- **Rejected for the first scan path:** macOS users overwhelmingly do not set XDG variables, so the implicit defaults (`~/.local/share`, `~/.config`) become the effective paths. We list `~/.config/agentcookie/manifests/` as the second-priority path explicitly, which serves the XDG community without forcing the rest of macOS to learn XDG.

---

## Dependencies and Prerequisites

- v0.13.0-beta.1 is shipped and the secrets bus wire format is stable.
- The existing `internal/secretsbus/` package is the substrate; this plan extends it.
- `github.com/BurntSushi/toml` is already a dependency.
- No new external Go modules required.
