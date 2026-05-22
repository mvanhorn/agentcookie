---
title: "feat: agentcookie secrets bus (cross-CLI auth-token sync via .env-shaped sealed sidecar)"
status: active
type: feat
created: 2026-05-22
---

# feat: agentcookie secrets bus (cross-CLI auth-token sync via .env-shaped sealed sidecar)

## Problem Frame

agentcookie solves the cookie half of the "your laptop is logged in, your sink isn't" problem. The auth-token half is still manual: every CLI that needs API tokens, OAuth refresh tokens, signing keys, or vendor API keys has its own bespoke storage layout under `~/.config/<cli>/`, and the friend has to either rerun every OAuth login on the sink or hand-copy each CLI's secrets via scp. Today, 36 PP CLIs are installed locally, and they store auth across at least nine distinct shapes (TOML, JSON, multi-file token bundles, sqlite blobs, browser-session-proof JSON, raw env vars, OS keychain items, .pem signing keys, and combinations). The Tesla case alone uses eight files across two directories.

The same problem applies to CLIs that are not part of the Printing Press ecosystem at all. `tesla-control`, `gh`, `aws`, `gcloud`, third-party scripts a friend writes, all have the same "logged in on the laptop, needs to be logged in on the sink" gap. A solution scoped to PP CLIs only would leave that ecosystem unserved.

This plan proposes agentcookie defines a standard, language-agnostic secrets-bus format (sealed-optional `.env` sidecars under a known path), syncs them source-to-sink via the existing AES-256-GCM Tailscale channel, and ships a tiny reader library so any CLI (PP or not) can opt in by reading one well-known file. It also commits to a concrete inventory of the 36 locally installed PP CLIs as the empirical baseline for the format design.

## Summary

Add a "secrets bus" to agentcookie that mirrors how cookies work today: friend authenticates a CLI once on the laptop, agentcookie watches a known path, ships a sealed payload to the sink, sink unseals and writes per-CLI `.env` files at predictable locations. PP CLIs (and any other CLI) opt in by reading those `.env` files at startup using whatever dotenv parser their language ships. Cryptographic signing keys that must not leave a single machine are marked local-only via a manifest convention and excluded from sync.

Source of truth on the format and the directory layout lives in agentcookie. Adoption in each individual CLI is its own PR in its own repo and explicitly out of scope here, with a migration runbook for owners.

## Audience

Two readers in mind:

- **Friends who use one or more PP CLIs from a sink machine**: they want "one login on the laptop, agent works on the sink" applied to API tokens the same way it already applies to cookies. They should not have to know about agentcookie internals; they should just see their tokens appear on the sink after they finish OAuth on the laptop.
- **CLI maintainers (PP CLI authors + non-PP-CLI authors)**: they want a tiny stable contract: "read this file at this path, the variables you need will be there, you do not have to care about the encryption-at-rest story." The reader library should be ~20 lines for them to integrate.

## Requirements

- R1. agentcookie defines a public, documented format for storing per-CLI secrets that any CLI in any language can consume with a standard `.env` parser, without linking an agentcookie library.
- R2. Standardized storage path: `~/.agentcookie/secrets/<cli-name>/secrets.env` (plus optional sealed twin `secrets.env.sealed` when the v0.12 master key is set up).
- R3. agentcookie source-side watches the secrets path tree for changes and pushes the diff to the sink through the existing pair-derived AES-256-GCM channel, mirroring the cookies sync flow.
- R4. Sink-side writes the secrets files at the same path layout on the sink machine, sealed under the v0.12 master key when sealing is enabled, plaintext otherwise.
- R5. A manifest convention identifies secrets that must stay machine-local (signing keys, OS-provisioned credentials, anything whose threat model rules out serialization) and excludes them from sync. Manifest lives at `~/.agentcookie/secrets/<cli-name>/manifest.toml`.
- R6. Reference reader libraries: a Go module under agentcookie that resolves the sealed-or-plaintext file and returns a `map[string]string` of env vars, and a Python equivalent that does the same for non-Go consumers.
- R7. `agentcookie secret` subcommand: `list`, `get`, `set`, `rm`, `import-from` (one-shot ingestion from an existing `~/.config/<cli>/<file>` into the standard shape). Friend-facing tool for the cases agentcookie cannot auto-detect.
- R8. `agentcookie doctor` reports the secrets-bus health: count of CLIs registered, count of secrets in the bus, sealed vs plaintext mode, sync state.
- R9. Concrete audit deliverable: `docs/audits/2026-05-22-pp-cli-auth-inventory.md` documenting all 36 locally installed PP CLIs' current auth storage shapes (filename, encoding, secret types held, sync-safe vs local-only classification).
- R10. Non-PP-CLI support proven by a worked example: at least one third-party CLI (candidate: `gh`) demonstrated reading from the secrets bus via a thin shim, documented in a runbook.
- R11. Migration runbook for PP CLI authors: how to adopt the format in their existing CLI without breaking current users.
- R12. Existing v0.12.0-beta.6 installs see no behavior change. The secrets bus is additive and opt-in per CLI.

## Audit (Phase 1)

Implementation Unit U1 produces the actual inventory. The plan-time audit below captures the variance I sampled across 13 of the 36 PP CLIs to shape the format design. None of these contain real secret values; the columns describe file shape and what kind of secret is being stored.

| PP CLI | Files under ~/.config/<name>/ | Secret types observed |
|---|---|---|
| airbnb-pp-cli | config.toml, cookies.json | session cookies, account ID |
| ebay-pp-cli | config.toml, browser-session-proof.json | session cookies, browser identity |
| ordertogo-pp-cli | config.toml, cookies.json, active-cart.json | session cookies |
| expensify-pp-cli | config.toml | OAuth bearer (assumed) |
| espn-pp-cli | config.toml, watchlist.json | API key (presumed) |
| openart-pp-cli | config.toml, browser-session-proof.json | OAuth bearer + browser identity |
| suno-pp-cli | config.toml, browser-session-proof.json | OAuth bearer + browser identity |
| tesla-pp-cli | config.toml, auth.json | OAuth bearer + refresh token (split between two files) |
| booking-com-pp-cli | config.toml | account credentials |
| superhuman-pp-cli | config.toml, tokens.json, send-queue.json | OAuth tokens (multi-file) |
| linear-pp-cli | store.db | sqlite-stored token blob |
| table-reservation-goat-pp-cli | session.json | session payload |
| (Tesla extra) ~/.tesla/ | fleet-token, fleet-token.refresh, fleet-client-id, fleet-client-secret, fleet-partner-token, snowflake-private.pem, snowflake-public.pem | OAuth + refresh + partner-app creds + ECDSA signing keypair |

Patterns visible across the sample:

- `config.toml` is the most common shape (10 of 13), but its contents vary widely: some hold OAuth bearers, some hold API keys, some hold no secrets at all (just chrome cookie DB paths).
- Refresh tokens often live in a separate file from the access bearer (Tesla: `auth.json` vs `config.toml`; Superhuman: `tokens.json` vs `config.toml`).
- "Browser session proof" JSON is a recurring pattern for CLIs that present as a browser to an API: a snapshot of cookies, headers, and timing data used to look human.
- SQLite (linear-pp-cli) is used when the CLI also caches issue data; auth and content share the file.
- Outside `~/.config/`, Tesla also uses `~/.tesla/` for Fleet API artifacts and a signing keypair. This second location pattern (`~/.toolname/`) is common for CLIs that predate the XDG convention.

The full inventory (U1) is what unblocks the per-CLI migration plan. The plan-time sample is sufficient to shape the format.

## External reference standards

Skimmed the conventions used by other CLI auth ecosystems to anchor the format choice:

- **`.env` / dotenv**: line-oriented `KEY=VALUE`. Parsers exist for every mainstream language (Go: `joho/godotenv`, Python: `python-dotenv`, Node: `dotenv`, Ruby: `dotenv`). Universal, friend-editable, no encryption story by default. Used by last30days as observed in `~/last30days-digg-bridge/.env`. This is the format we adopt.
- **`gh` (GitHub CLI)**: `~/.config/gh/hosts.yml`, YAML, OAuth bearer per host. Single tool reads it; not designed for external consumers.
- **`aws`**: `~/.aws/credentials` (INI), `~/.aws/config` (INI), one shared layout that many tools (terraform, ansible, AWS SDKs) consume. INI with profile sections, no encryption at rest.
- **`gcloud`**: complex SQLite at `~/.config/gcloud/credentials.db`. Single-tool internal.
- **`stripe`**: `~/.config/stripe/config.toml`, TOML, single-tool.
- **`vault`**: `~/.vault-token`, single token file, plain text.

Takeaway: every successful "multi-consumer" auth convention (aws credentials being the strongest example) is plaintext at rest, line-oriented or section-oriented, has a documented schema, and trusts the OS file permissions for security. None of them encrypt at rest by default; encryption is left to disk-level encryption (FileVault, LUKS) or runtime secret stores (Vault, 1Password CLI).

Our deviation from this pattern: we offer optional sealing under the agentcookie master key for friends who want at-rest encryption on the sink, while keeping the plaintext-by-default story so PP CLIs (and gh, aws, etc.) can read with their existing dotenv loaders.

---

## Scope Boundaries

In scope:

- Format spec + reference Go and Python reader libraries.
- agentcookie source-side detection, sink-side write, sealed-optional storage.
- `agentcookie secret` subcommand surface.
- Doctor visibility.
- Full inventory of 36 locally installed PP CLIs.
- Migration runbook for CLI authors (PP and non-PP).
- One worked example of a non-PP CLI (gh) reading from the bus via shim.

### Deferred to Follow-Up Work

- Implementing the reader in each individual PP CLI. That work lives in each CLI's repo and follows its own release cadence. The migration runbook is the bridge.
- Web UI for managing secrets. Today: `agentcookie secret` CLI is the interface.
- Cross-machine signing-key generation flow (multi-sink ECDSA key enrollment for Tesla-style flows). The local-only marker keeps these out of sync; multi-machine enrollment is a separate plan.
- Backup/restore of the secrets bus (cloud sync, encrypted exports). Out of scope; FileVault + Time Machine cover the friend-side need today.
- Secret rotation / expiry notifications. The bus stores what's written; rotation is each CLI's responsibility.

---

## Key Technical Decisions

- **.env format, plaintext by default, sealed-optional under master key.** Plaintext .env is the lingua franca: every mainstream language has a parser, friends can $EDITOR the file, no agentcookie linkage required for adoption. Sealed-optional gives at-rest protection on the sink without breaking the plaintext contract for callers that just want env vars. Sealed mode requires either the reader library (which transparently unseals) or shelling out to `agentcookie secret get` (which prints to stdout).
- **Path: `~/.agentcookie/secrets/<cli-name>/secrets.env`.** One agentcookie-owned tree, predictable, easy to back up, easy for the friend to inspect. Matches the per-cli-name pattern but lives under agentcookie's umbrella so the friend doesn't have to know about each CLI's idiosyncratic location.
- **Sync mirrors cookies source-to-sink.** The friend authenticates once on the laptop. agentcookie source watches `~/.agentcookie/secrets/` via fsnotify, ships changes to the sink through the existing pair-derived channel, sink writes them at the same path. Same security boundary as cookies, same blocklist/allowlist semantics apply.
- **Manifest file marks local-only secrets.** `~/.agentcookie/secrets/<cli-name>/manifest.toml` carries a `sync = true|false` boolean per file or per-key. Default is `sync = true`. ECDSA private keys, OS-provisioned credentials, anything with a single-machine threat model gets `sync = false` and stays out of the wire envelope.
- **agentcookie owns the bus, CLIs own their integration.** agentcookie writes the file at the standard path. PP CLIs and non-PP CLIs each PR their own integration in their own repo. We do not vendor agentcookie code into individual CLIs; we publish a small reader module they can import. This matches how `aws-sdk` reads `~/.aws/credentials` without depending on the AWS CLI binary.
- **Non-PP-CLI support is a first-class requirement, not an afterthought.** The format is documented as a public contract. The worked `gh` example proves the path. Future third-party adoption needs nothing more than the docs.
- **Go reader is the canonical implementation; Python reader follows the same semantics.** Two languages cover ~all the PP CLI surface (most are Go) and the broader scripting use case. Other languages can implement the contract from the spec.
- **No web UI, no remote API surface in v1.** `agentcookie secret` CLI plus direct file editing covers the workflows.

---

## High-Level Technical Design

Data flow mirrors the existing cookies bus:

```
laptop                                              sink
======                                              ====

friend runs `tesla-pp-cli auth login`
  CLI writes its own credentials to its own
  config dir under ~/.config/tesla-pp-cli/
  |
  | (friend optionally runs `agentcookie secret import-from
  |   ~/.config/tesla-pp-cli/auth.json --as tesla-pp-cli`,
  |  OR the CLI writes directly to the standard path)
  v
~/.agentcookie/secrets/tesla-pp-cli/
  ├── secrets.env          (plaintext, KEY=VALUE)
  ├── manifest.toml        (sync = true/false per file or per-key)
  └── secrets.env.sealed   (optional, sealed under master key)
  |
  | fsnotify catches the write
  v
agentcookie source --watch
  |
  | filters manifest (sync=false files dropped), seals payload
  v
+----- HTTPS over Tailscale (AES-256-GCM, replay-defended) -----+
                                                                |
                                                                v
                                              agentcookie sink (LaunchAgent)
                                                |
                                                | writes to ~/.agentcookie/secrets/tesla-pp-cli/
                                                | optionally seals under master key
                                                v
                                              tesla-pp-cli on the sink reads
                                              ~/.agentcookie/secrets/tesla-pp-cli/secrets.env
                                              via godotenv at startup. Done.
```

This illustrates the intended approach and is directional guidance for review, not implementation specification.

Format spec at a glance:

```
# ~/.agentcookie/secrets/<cli-name>/secrets.env
# Lines starting with # are comments.
# KEY=VALUE format. Values may be quoted with " or '.
# Multi-line values supported via shell-style backslash continuation.

TESLA_OAUTH_BEARER=eyJ...redacted...
TESLA_OAUTH_REFRESH=eyJ...redacted...
TESLA_FLEET_CLIENT_ID=redacted
TESLA_FLEET_CLIENT_SECRET=redacted
```

```toml
# ~/.agentcookie/secrets/<cli-name>/manifest.toml
schema_version = 1
display_name = "Tesla PP CLI"

[sync]
# Whole-file default. Individual keys can opt out via [sync.keys].
default = true

[sync.keys]
# Per-key overrides. Anything ECDSA-signing-key-shaped stays local.
TESLA_SNOWFLAKE_PRIVATE_KEY_PEM = false
```

Per-CLI side: the CLI links `pkg/agentcookiesecret` (Go) or `agentcookie_secret` (Python), calls `Load("tesla-pp-cli")`, gets a `map[string]string`. Library handles sealed-vs-plaintext detection, falls back to environment variables and the CLI's existing config file in priority order so the migration is non-breaking.

---

## Implementation Units

### U1. Audit and inventory all 36 PP CLIs

**Goal:** Walk every `~/.config/*-pp-cli/` dir and every `~/.<name>/` dir referenced by an installed PP CLI binary. Document the storage shape, file-by-file, plus the type of secret each holds and whether it is safe to sync (general OAuth bearer / refresh token) or must stay local (signing key, OS-provisioned cred).

**Requirements:** R9.

**Dependencies:** none.

**Files:**
- `docs/audits/2026-05-22-pp-cli-auth-inventory.md` (created)

**Approach:** For each installed PP CLI, run a structured probe: list files, identify likely secret-bearing fields by name (token, key, secret, bearer, refresh, pem, credential), and classify each one. Cross-reference with each CLI's `--help` output to confirm what it expects to find. Do NOT capture secret values; the deliverable is structure only. Output is a single markdown table organized by CLI, with columns: file path, format (toml/json/sqlite/env/pem/other), secret kinds present, sync-safety classification, current secret-loading mechanism (read at startup, looked up in OS keychain, expects env var, etc.).

**Patterns to follow:** the plan-time sample table above is the shape; expand it to all 36 CLIs and add any non-PP CLIs the friend cares about (tesla-control, gh, etc.) as a separate appendix.

**Test scenarios:** Test expectation: none — research deliverable.

**Verification:** The audit document exists, covers all 36 PP CLIs, and the sync-safety classification has explicit rationale for each "local-only" decision (not just a list).

---

### U2. Format specification document

**Goal:** Write the public, versioned contract for the secrets bus: directory layout, `.env` line semantics, `manifest.toml` schema, sealed-file shape, file mode requirements, atomic-write requirements, reserved key names, error semantics.

**Requirements:** R1, R2, R5.

**Dependencies:** U1 (so the format absorbs the patterns observed).

**Files:**
- `docs/spec-agentcookie-secrets-bus-v1.md` (created)

**Approach:** The spec is a single document, sectioned: scope, directory layout, file formats (.env grammar plus the existing v0.12 sealed-file format reused verbatim), manifest schema with examples, security boundary statement, versioning rule (schema_version field in manifest). Include a "what this is NOT" section: not a generic secret store, not a credential issuer, not a rotation system; it's a transport + format for secrets the CLI already has.

The .env grammar follows the dotenv loose convention (KEY=VALUE per line, # comments, optional quotes, no interpolation, no nested objects). Explicitly forbid features that fragment across parsers: variable interpolation (`$OTHER`), multi-line block syntax beyond backslash continuation, JSON-in-value.

**Patterns to follow:** the existing `docs/protocol.md` for the wire-format spec style. Same level of formality.

**Test scenarios:** Test expectation: none — documentation deliverable, but every grammar rule must have at least one example in the spec doc.

**Verification:** The spec doc covers every requirement R1, R2, R5 in named subsections. A reader who never touched agentcookie can read this spec and write a conforming reader in any language.

---

### U3. Source-side fsnotify watcher + push pipeline

**Goal:** agentcookie source detects changes to `~/.agentcookie/secrets/**` and pushes filtered payloads to the sink through the existing transport.

**Requirements:** R3, R5.

**Dependencies:** U2.

**Files:**
- `internal/secretsbus/watcher.go` (created)
- `internal/secretsbus/watcher_test.go` (created)
- `internal/secretsbus/payload.go` (created, defines the on-wire secret-payload struct)
- `internal/cli/source.go` (modified, registers the secrets watcher alongside the cookies watcher)
- `internal/protocol/envelope.go` (modified if needed to carry an optional secrets payload alongside cookies; otherwise a new envelope type)

**Approach:** Mirror `internal/watcher/` (the Chrome Cookies watcher). The secrets watcher subscribes to fsnotify events for the secrets root, debounces them (same 500ms debounce the cookies watcher uses), reads the affected `secrets.env` files plus their manifests, drops files / keys marked `sync = false`, and posts the payload through the existing protocol layer. Sealed source-side files are passed through opaque; the sink already has the same master key (via the pair-derived shared secret + the v0.12 sealing layer) so it can unseal where appropriate.

**Patterns to follow:** the Chrome Cookies watcher in `internal/watcher/cookies.go` (debounce, fsnotify subscribe, push to source-state) is the closest analog. Reuse its debounce helpers.

**Execution note:** Start with a failing integration test that drops a file at `~/.agentcookie/secrets/foo/secrets.env`, asserts the source's `last_push` count includes the new file's keys.

**Test scenarios:**
- happy path: write `secrets.env` containing 3 keys, source push includes those 3 keys, no other changes.
- happy path: write `manifest.toml` with `[sync.keys] FOO = false`, source push omits `FOO` but includes other keys.
- edge case: write a `secrets.env` over 256KB (size limit); source rejects with a clear error and drops the push, does not crash.
- edge case: drop a `manifest.toml` only (no `secrets.env` yet); source treats as no-op until the env file appears.
- edge case: rapid sequential writes (5 writes in 200ms); debounce coalesces to one push.
- error path: corrupted manifest.toml; source logs error, does NOT push the malformed file, continues watching.
- integration: combined with the existing cookies watcher, a Chrome-cookie change and a secrets change in the same second produce two distinct pushes (or one combined push) without dropping either.

**Verification:** `agentcookie source --once` with a populated secrets dir produces a sink-state entry showing the secrets count and per-CLI breakdown.

---

### U4. Sink-side receive + write + sealed-optional storage

**Goal:** The sink accepts the secrets payload from the source, writes it to `~/.agentcookie/secrets/<cli-name>/secrets.env` on the sink, optionally seals it under the master key.

**Requirements:** R4, R12.

**Dependencies:** U2, U3.

**Files:**
- `internal/secretsbus/writer.go` (created)
- `internal/secretsbus/writer_test.go` (created)
- `internal/cli/sink.go` (modified, dispatches secrets-bus payloads to the writer)

**Approach:** Symmetric to the cookies sidecar writer. On `/sync`, after the existing cookies-sidecar + adapter pushes run, if the envelope carries a secrets payload, write each per-CLI file via atomic rename (write to `.tmp`, fsync, rename). Mode 0600 on every file. If the v0.12 master key is configured AND the operator opted into sealing at install time, additionally write a `.sealed` twin and remove the plaintext (the reader library knows to try `.sealed` first). The master key path mirrors the v0.12 sealed-sidecar pattern at `internal/chrome/sidecar.go`.

R12 regression guard: when the source sends a payload with NO secrets section, the sink writes nothing new and the existing cookies / adapter behavior is byte-identical to v0.12.0-beta.6.

**Patterns to follow:** `internal/chrome/sidecar.go` for the sealed write path; `applyEnvelopeToSink` in `internal/cli/sink.go` for the receive-and-route shape.

**Test scenarios:**
- happy path: sink receives payload with 2 CLIs, writes both files, file mode 0600, atomic.
- happy path: master key present + sealing enabled, sink writes only `secrets.env.sealed`, no plaintext on disk.
- happy path: master key present + sealing NOT enabled (the v0.12.0-beta.3 default), sink writes plaintext only. R12 regression guard.
- edge case: payload contains a per-CLI dir name with `..` or `/`; sink refuses, logs the rejection, does not write outside the secrets root.
- edge case: existing local file at the target path with NEWER mtime; sink writes anyway (source is authoritative, that's the sync model) but logs the conflict.
- integration: round-trip: source writes secrets.env, source pushes, sink writes the file, sink-side reader library reads back the same KEY=VALUE pairs.
- regression: payload without secrets-section produces zero new files; cookies and adapter writes proceed unchanged. R12.

**Verification:** end-to-end on the Mac mini sink: source writes `~/.agentcookie/secrets/test-cli/secrets.env` with `FOO=bar`, sync runs, sink has the same file at the same path with the same content (or its sealed twin), file mode 0600.

---

### U5. Go reader library (`pkg/agentcookiesecret`)

**Goal:** Importable Go module that resolves sealed-or-plaintext secrets for a given CLI name, falls back through a sensible priority chain, and exposes the result as a `map[string]string`.

**Requirements:** R6.

**Dependencies:** U2, U4.

**Files:**
- `pkg/agentcookiesecret/load.go` (created)
- `pkg/agentcookiesecret/load_test.go` (created)
- `pkg/agentcookiesecret/doc.go` (created — go doc for external consumers)

**Approach:** Public function `Load(cliName string) (map[string]string, error)`. Resolution priority: 1) `~/.agentcookie/secrets/<cli-name>/secrets.env.sealed` if master key available; 2) `~/.agentcookie/secrets/<cli-name>/secrets.env` plaintext; 3) the caller's existing config dir at `~/.config/<cli-name>/<file>` if explicitly registered via `LoadWithFallback`; 4) process environment variables. Returns the merged result with later sources NOT overriding earlier (so the bus wins over env). Sealed-file unseal reuses `internal/keystore` from agentcookie via the public surface.

Public surface is intentionally tiny: `Load`, `LoadWithFallback`, `WatchForChanges` (channel-based, optional for long-lived daemons that want to pick up rotated secrets without restart).

**Patterns to follow:** the existing `pkg/sidecar` module shape in agentcookie. Same public-API minimalism. Same go-doc style.

**Execution note:** Test-first. Write the public surface tests against a temp HOME before the implementation.

**Test scenarios:**
- happy path: only plaintext `secrets.env` present, Load returns the keys.
- happy path: sealed-only mode, master key available, Load returns the unsealed keys.
- happy path: sealed-only mode, master key NOT available, Load returns an error naming the missing key path.
- edge case: both plaintext and sealed present (transitional); Load prefers sealed, plaintext ignored.
- edge case: empty secrets.env (zero keys); Load returns an empty map and nil error.
- edge case: CLI name with invalid characters (`..`, `/`); Load returns an error before touching the filesystem.
- error path: malformed `.env` line (e.g. `KEY` without `=`); Load returns an error pointing at the line number.
- integration: written by U4's sink-side writer, read by this library; round-trip is byte-identical for keys and values.

**Verification:** Library can be `go get`'d in a separate test repo, imported, and used to read a manually-populated `~/.agentcookie/secrets/test-cli/secrets.env`. `go doc pkg/agentcookiesecret` renders cleanly.

---

### U6. Python reader library (`agentcookie_secret`)

**Goal:** Python equivalent of U5 for non-Go consumers (scripts, ad-hoc tools, agent runtimes that ship Python).

**Requirements:** R6.

**Dependencies:** U2, U4, U5 (so semantics are pinned to the Go reference).

**Files:**
- `clients/python/agentcookie_secret/__init__.py` (created)
- `clients/python/agentcookie_secret/load.py` (created)
- `clients/python/agentcookie_secret/test_load.py` (created)
- `clients/python/pyproject.toml` (created)
- `clients/python/README.md` (created — install + use)

**Approach:** Same resolution priority as the Go reader. Single public function `load(cli_name: str) -> dict[str, str]`. Sealed-file unseal calls out to `agentcookie secret get` (the U7 subcommand) so the Python module does not need to vendor the encryption layer. Falls back gracefully when the agentcookie binary is not on PATH (returns plaintext-only).

**Patterns to follow:** the `python-dotenv` package as the parser dependency; same minimalist public surface as U5.

**Execution note:** Test-first. Mirror the Go test scenarios where applicable.

**Test scenarios:**
- happy path: plaintext-only load returns dict with expected keys.
- happy path: sealed-only mode, agentcookie binary on PATH, load shells out and returns the unsealed dict.
- edge case: agentcookie binary not on PATH AND sealed-only; load raises a clear `AgentcookieSecretError` naming the missing binary.
- error path: malformed `.env` value; raises with line number.
- integration: write a file with U4's writer; read it with this library; values match.

**Verification:** `pip install -e clients/python` works; `from agentcookie_secret import load` returns the expected dict for a manually populated path.

---

### U7. `agentcookie secret` CLI subcommand

**Goal:** Friend-facing tool for managing secrets in the bus when auto-detection from a CLI's existing config dir is not possible (yet).

**Requirements:** R7.

**Dependencies:** U2, U5.

**Files:**
- `internal/cli/secret.go` (created)
- `internal/cli/secret_test.go` (created)
- `docs/quickstart.md` (modified, add a "Storing secrets for CLIs" subsection)

**Approach:** Cobra subcommand with the standard verbs:
- `agentcookie secret list` — print a tree of `<cli-name>` -> key list (no values).
- `agentcookie secret get <cli-name> <key>` — print value to stdout (used by the Python reader's shell-out path).
- `agentcookie secret set <cli-name> <key>` — prompt on stdin for the value (TTY) or read from stdin (pipe).
- `agentcookie secret rm <cli-name> [<key>]` — remove one key or the whole CLI dir.
- `agentcookie secret import-from <path> --as <cli-name>` — one-shot ingest from an existing config file (JSON, TOML, env-shaped) into the standard layout. Heuristic field-name mapping for the common shapes documented in U1's audit; unknown fields land under their original key name with a leading `_` to flag manual review.

Mode 0600 on every write. Sealed-or-plaintext respected per the v0.12 sealing setting.

**Patterns to follow:** the existing `agentcookie wizard ...` subcommand structure for shape; the `agentcookie doctor` subcommand for printable output.

**Test scenarios:**
- happy path: `set` then `get` round-trips a value.
- happy path: `list` shows three CLIs with their key names but no values.
- happy path: `import-from` reads `~/.config/tesla-pp-cli/auth.json`, maps `access_token` → `TESLA_OAUTH_BEARER` (per the audit's heuristic table), `refresh_token` → `TESLA_OAUTH_REFRESH`, writes the standard layout.
- edge case: `import-from` encounters a JSON field it cannot map; writes `_unknown_field_name=value` with a warning printed to stderr.
- error path: `set` to a malformed CLI name (`../foo`); rejects before touching the filesystem.
- integration: `set` then read via the U5 Go library; values match.

**Verification:** `agentcookie secret list` after a sync from the laptop shows the same CLI list and key names on the sink.

---

### U8. Doctor coverage

**Goal:** `agentcookie doctor` reports secrets-bus health alongside the existing checks.

**Requirements:** R8.

**Dependencies:** U4.

**Files:**
- `internal/cli/doctor.go` (modified)
- `internal/cli/doctor_test.go` (modified)

**Approach:** Add a "Secrets bus" check that reports: registered CLI count, total key count, sealed vs plaintext mode, sync-state freshness (mtime of newest file in `~/.agentcookie/secrets/`). WARN when sealed mode is configured but no `.sealed` files exist (sync hasn't run yet). SKIPPED on source-only installs since the secrets bus is sink-side too.

**Patterns to follow:** the v0.12.0-beta.3 "Adapter coverage" check shape at `internal/cli/doctor.go`.

**Test scenarios:**
- happy path: 3 CLIs in the bus, plaintext mode, OK reports the counts.
- happy path: sealed mode configured + sealed files present, OK reports "sealed".
- WARN path: sealed mode configured but no `.sealed` files yet, WARN with remediation pointer.
- SKIPPED path: source-only install with no secrets bus dir, reports SKIPPED.
- regression: existing 10 checks still present; new check raises the envelope count to 11. Update the existing doctor envelope test accordingly.

**Verification:** `agentcookie doctor --json` on a sink with 3 CLIs in the bus produces a valid envelope with the new entry; doctor exit code stays 0 when all-green.

---

### U9. Worked example: gh CLI reads from the secrets bus

**Goal:** Prove the non-PP-CLI case end-to-end with the GitHub CLI as the worked example. Friend stores `gh` OAuth token in the bus; `gh` reads it via a shim wrapper.

**Requirements:** R10.

**Dependencies:** U2, U4, U5 (or U6).

**Files:**
- `docs/runbook-secrets-bus-gh-example.md` (created)
- `examples/gh-shim/gh-shim` (created, executable shell wrapper)
- `examples/gh-shim/README.md` (created)

**Approach:** The shim is a 10-line shell script: `agentcookie secret get gh GH_TOKEN | GH_TOKEN=$(cat) exec gh "$@"`. The runbook walks the friend through: 1) on laptop, `gh auth login` as today; 2) `agentcookie secret import-from ~/.config/gh/hosts.yml --as gh` ingests the OAuth bearer; 3) source pushes to sink; 4) on sink, the friend either calls the shim wrapper or `eval "$(agentcookie secret env gh)"` to load before invoking `gh`.

Out-of-band note: a proper non-shim integration would require `gh` itself to read from the bus, which is upstream PR territory not in this plan. The shim demonstrates the format works for unmodified third-party CLIs today.

**Patterns to follow:** `docs/runbook-v0.11-adapter-cookie-push.md` shape (short, concrete, command-by-command).

**Test scenarios:** Test expectation: none — runbook + example artifact. Manual verification in U11.

**Verification:** Following the runbook from scratch on a clean sink, `ssh sink 'gh-shim issue list -R mvanhorn/agentcookie'` returns the live issue list without any `gh auth login` on the sink.

---

### U10. Migration runbook for CLI authors

**Goal:** Concrete recipe a PP CLI author (or any CLI author) can follow to adopt the secrets bus without breaking existing users.

**Requirements:** R11.

**Dependencies:** U2, U5, U6.

**Files:**
- `docs/runbook-secrets-bus-adoption.md` (created)

**Approach:** The runbook covers the three integration patterns observed in the U1 audit: 1) Go CLIs that read a single config.toml or auth.json today (most PP CLIs); 2) Python or scripting CLIs that read env vars; 3) CLIs that store auth in their own dir outside `~/.config/` (Tesla-style). For each pattern, the recipe gives: a) the call into the reader library, b) the fallback priority that preserves existing behavior, c) a 5-line change diff template the author can paste into their CLI's startup code.

Includes a "what NOT to do" section: do not seal at the CLI layer (let agentcookie own that), do not rewrite the file (the bus is source-of-truth, the CLI is read-only against the bus), do not interpolate values into other env vars (the format forbids it for parser compatibility).

**Patterns to follow:** `docs/runbook-v0.11-adapter-cookie-push.md` for the "how to add an adapter" shape.

**Test scenarios:** Test expectation: none — documentation deliverable, validated by U11.

**Verification:** Pick one PP CLI from the U1 audit (candidate: `expensify-pp-cli`, simple TOML shape, single OAuth bearer) and walk the runbook against its source. The migration patch is under 20 lines and existing users see no behavior change.

---

### U11. End-to-end dry-run + cut release

**Goal:** Validate the full secrets-bus flow end-to-end on a live source + sink pair; cut the release.

**Requirements:** all.

**Dependencies:** U1-U10.

**Files:**
- `CHANGELOG.md` (modified, new version section)
- `docs/dry-run-2026-MM-DD.md` (created at dry-run time)

**Approach:** Reset the Mac mini sink (same flow as the v0.12.0-beta.3/5/6 dry-runs). On the laptop, run `agentcookie secret import-from ~/.config/tesla-pp-cli/auth.json --as tesla-pp-cli`, watch the source push fire, verify the sink wrote the file at the standard path with mode 0600 and the values match. Run the gh shim from the sink to confirm the non-PP-CLI case. Capture friction in a dated dry-run doc; ship as the next v0.12.0-beta.N or, if the surface justifies it, cut v0.13.0-beta.1.

**Execution note:** Capture the friction log inline, same pattern as the 2026-05-19 and 2026-05-21 dry-runs. Items the audit reveals after-the-fact go in the friction log, not back into this plan.

**Test scenarios:** Test expectation: none — validation gate. Per-unit tests in U1-U10 carry the correctness load.

**Verification:** dated dry-run doc committed; release tag published; sample CLI (expensify-pp-cli per U10) demonstrably reads its secrets from the bus on the sink with no manual config.

---

## System-Wide Impact

- **Source side**: gains a new fsnotify watcher subscribing to `~/.agentcookie/secrets/`. Memory + CPU cost is the same shape as the existing cookies watcher (debounced, idle most of the time).
- **Sink side**: gains a new writer path that fires per `/sync` when the payload carries a secrets section. Sealed writes reuse the v0.12 master key when sealing is enabled.
- **PP CLI ecosystem**: gains an optional secrets source. Each CLI's adoption is its own PR and goes at its own pace. Non-adopters continue to work via their existing config dirs.
- **Friend onboarding**: gets a new chapter (`Storing secrets for CLIs`) in the quickstart. One additional command (`agentcookie secret import-from ...`) per CLI that uses the bus.
- **Existing v0.12.0-beta.6 installs**: no behavior change unless they create `~/.agentcookie/secrets/<cli-name>/` themselves. R12 regression guards live in U3 and U4 tests.

## Risks and Mitigations

- **Risk**: `.env` plaintext at rest is a real security regression vs OS keychain for some friends' threat models. Mitigation: sealed-optional mode under the v0.12 master key; default sealing posture documented in the spec; friends with stricter requirements opt in.
- **Risk**: Sync overwrites a sink-side secret that's actually fresher (e.g. the sink ran an OAuth refresh and updated its token while the source hadn't yet). Mitigation: per-key manifest entry `sync = false` for keys the sink is allowed to mint (refresh tokens that rotate per-machine). Documented in the spec; default is `sync = true` since most secrets are one-source.
- **Risk**: Heuristic field mapping in `import-from` produces wrong key names (e.g. mistakes a session ID for an API key). Mitigation: unknown fields write `_<original_name>` and print a stderr warning, so the friend can review and rename. The Tesla case in U7's test scenarios is the worst-case example.
- **Risk**: The audit reveals a PP CLI whose secret-storage pattern fundamentally cannot fit the `.env` shape (e.g. binary blobs over 256KB, sqlite-only storage with constraints on the schema). Mitigation: U1 surfaces these explicitly; the format spec's "what this is NOT" section documents the boundary; affected CLIs can adopt later or stay manual.
- **Risk**: Cross-repo coordination cost (each PP CLI adopting in its own PR). Mitigation: U10's runbook is the bridge; agentcookie does not gate on adoption; the bus is useful from day one for any single CLI that adopts.
- **Risk**: Non-PP-CLI integration (e.g. `gh`) requires shim wrappers since upstream tools won't link our reader. Mitigation: documented limitation; the shim pattern is generic and one-line. Future upstream integration is out of scope here.

## Acceptance Criteria

- A friend who runs `agentcookie secret import-from ~/.config/tesla-pp-cli/auth.json --as tesla-pp-cli` on the laptop sees the secret payload land at `~/.agentcookie/secrets/tesla-pp-cli/secrets.env` on the sink within one sync interval, with mode 0600, with the OAuth bearer correctly mapped to a `TESLA_OAUTH_BEARER` key.
- A Go PP CLI on the sink that imports `pkg/agentcookiesecret` and calls `Load("tesla-pp-cli")` sees the expected map.
- A Python script on the sink that imports `agentcookie_secret` and calls `load("tesla-pp-cli")` sees the same map.
- A non-PP CLI (`gh`) wrapped via the shim from U9 successfully runs `issue list` on the sink without `gh auth login` on the sink.
- `agentcookie doctor` reports the bus health correctly across the three states (no bus, plaintext bus, sealed bus).
- `docs/audits/2026-05-22-pp-cli-auth-inventory.md` lists all 36 installed PP CLIs with their current auth shapes and sync-safety classifications.
- An existing v0.12.0-beta.6 install upgrading to this version sees no behavior change unless they explicitly create `~/.agentcookie/secrets/`.
- The format spec is published in the repo and is sufficient for an external author to write a conforming reader without reading agentcookie source.

## Deferred Questions

- Should there be a daemon-mode reader-library variant that auto-reloads secrets on file change (for long-running agent processes that want zero-restart secret rotation)? Library exposes `WatchForChanges` channel in U5 as a hook; full daemon support deferred.
- Should `agentcookie secret import-from` learn to read from the OS Keychain directly (`security find-generic-password ...`)? Useful for CLIs that store there today (`gh` on macOS does). Deferred to v2 of the bus.
- Should the bus support secrets that are themselves binary (signed certificates, raw key bytes)? Today the format forbids it for parser compatibility. Friends with binary needs use the `agentcookie secret import-from --binary` path that base64-encodes into a `_BIN_<KEY>` env var with a marker. Documented in U2 spec.

## Origin

This plan was generated 2026-05-22 from a planning session where Matt asked for a feature in agentcookie that standardizes a format for PP CLIs to store secrets / auth tokens, with the Tesla CLI's eight-file auth layout as the worst-case example. The session also surfaced that the format must serve CLIs outside the Printing Press ecosystem, which shaped the format-spec-as-public-contract approach and the worked `gh` example.
