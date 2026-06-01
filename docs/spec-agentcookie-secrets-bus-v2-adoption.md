# agentcookie secrets bus v2: adoption standard

Status: draft (v2.0.0)
Companion document: [v1 wire format spec](spec-agentcookie-secrets-bus-v1.md)

This document specifies the v2 adoption standard layered on top of the v1 wire format. v1 defines how secrets travel between source and sink machines. v2 defines how a project declares participation so that agentcookie auto-discovers it without the user typing `agentcookie secret import-from`.

v2 is additive. Every v1 deployment continues to work unchanged. v2 introduces:

1. A project-side manifest file (`agentcookie.toml`).
2. A discovery loop that walks well-known paths to find those manifests.
3. A read-in-place mode that lets the project keep its existing `.env` as the source of truth.

## 1. Scope and non-scope

### 1.1 What this spec defines

- The filename, location, and grammar of the v2 adoption manifest.
- The well-known paths an agentcookie source machine scans for manifests.
- The precedence rules when the same project is declared in multiple locations.
- The collision rules when two projects declare the same name.
- The trust model and validation pipeline at discovery time.
- The mapping from the existing PP CLI `.printing-press.json` metadata into an in-memory v2 manifest (for backward compatibility).

### 1.2 What this spec does not define

- The wire format. See [v1 spec](spec-agentcookie-secrets-bus-v1.md).
- The sink-side ingestion behavior. v2 leaves the sink unchanged; only the source machine performs discovery.
- A central registry, web service, or signed-distribution channel.
- Cross-machine discovery semantics. Each source machine discovers from its own filesystem only.
- Secret rotation, expiry handling, or audit logging. Those remain the consuming CLI's responsibility.

## 2. Manifest file

### 2.1 Filename

The manifest is always named `agentcookie.toml`. Visible-by-default (no leading dot) so authors can grep their own repos for it without `ls -la`.

A v2 manifest is never named `manifest.toml`. The bare name `manifest.toml` is already used in v1 inside `~/.agentcookie/secrets/<name>/manifest.toml` for per-CLI sync overrides and is structurally different.

### 2.2 Discovery paths

The source machine scans these paths in priority order. The first occurrence of a given `name` wins. Lower-priority occurrences are recorded as soft-skipped with a stderr log.

| Priority | Path | Use case |
|----------|------|----------|
| 1 | `~/.agentcookie/manifests/*.toml` | Primary location. User or installer drops manifests here. |
| 2 | `~/.config/agentcookie/manifests/*.toml` | XDG-style alternative. Identical schema; equal status. |
| 3 | `/usr/local/share/agentcookie/manifests/*.toml` | System-installed manifests (homebrew, system installers). |
| 4 | `~/printing-press/library/*/.printing-press.json` | PP CLI auto-detect adapter. Synthesized into a v2 manifest at scan time. See section 7. |
| 5 | User-added paths via `agentcookie discover --add-path <dir>` | Escape hatch for non-standard layouts. |
| 6 | Legacy: existing entries in `~/.agentcookie/secrets/<name>/` | Synthetic registry entries for v1-imperative users. Read-in-place from the bus directory itself. |

### 2.3 File grammar

The manifest is TOML, strict subset of [TOML v1.0.0](https://toml.io/en/v1.0.0).

```toml
schema_version = 2                            # required; must be exactly 2
name = "last30days"                           # required; slug rules per section 4
display_name = "last30days"                   # required; human label
description = "Brand intelligence skill"      # optional; one-line, <= 200 chars
project_kind = "skill"                        # optional; "cli" | "skill" | "service" | "other"
homepage = "https://github.com/mvanhorn/last30days-skill"  # optional

# Exactly one [secrets.*] block.
[secrets.file]
path = "~/.config/last30days/.env"            # required when [secrets.file] is present

# Filter what gets shipped. Same shape as the v1 manifest [sync] table.
[sync]
default = true                                # optional; defaults to true

[sync.keys]
SETUP_COMPLETE = false                        # optional per-key overrides
FROM_BROWSER = false

# Optional. Declare consumer-env-var -> synced-bus-key mappings so a CLI that
# reads a different env var name than the key its secret was imported under is
# wired automatically, with no per-user `agentcookie secret alias` command.
[aliases]
TESLA_AUTH_TOKEN = "OAUTH_BEARER"             # CLI reads TESLA_AUTH_TOKEN; bus stores it as OAUTH_BEARER

# Optional. Carry arbitrary files (a multiline PEM, a TOML config) sealed, and
# materialize them on the sink as 0600 files under ~/.agentcookie/. Coexists
# with the single [secrets.*] block; not a second [secrets.*] block. See 5.4.
[[files]]
source = "~/.config/tesla-pp-cli/config.toml" # file to read + base64 into the bus
key = "TESLA_CONFIG_TOML"                      # wire key the base64 payload rides under
target = "tesla-pp-cli/config.toml"            # materialization path, relative, under ~/.agentcookie/
optional = false                              # true = opt-in (not carried unless enabled)
```

### 2.3.1 Aliases

The optional `[aliases]` table maps a consumer's declared env var name (the key) to the synced bus key that holds its value (the value). `agentcookie secret env <name>` applies these live on every call, so the mapping tracks token refreshes rather than going stale, and a CLI like `tesla-pp-cli` (which reads `TESLA_AUTH_TOKEN`) auto-connects to a bearer that agentcookie imported as `OAUTH_BEARER` for any user, with no manual command.

- Both the declared var (key) and the bus key (value) must be valid environment variable names: an initial letter or underscore, then letters, digits, or underscores. Invalid names are a hard parse error.
- An explicit local `agentcookie secret alias` entry for the same declared var overrides the manifest alias, so a user can always redirect the mapping.
- An alias whose bus key is absent emits nothing for that declared var (no-op), matching the local-alias behavior.

### 2.4 Reserved fields

These fields are reserved for v2.1+. Parsers MUST accept them without error but MUST NOT act on them until the corresponding feature ships:

- `signed_by` (top level): identity that authored this manifest. Used for signature verification in v2.1.
- `[secrets.command]` block: declares an exec to run for secret retrieval. Schema-reserved; parsers reject at runtime in v2.0 with "exec source not yet supported."
- `[secrets.keychain]` block: declares a macOS keychain lookup. Schema-reserved; same rejection rule as `[secrets.command]`.

### 2.5 Unknown fields

Unknown top-level fields produce a stderr warning (`unknown field 'X' in agentcookie.toml; ignored`) but do not fail parse. This is forward-compatibility: v2.1+ may add fields, and older agentcookie versions degrade gracefully.

## 3. Name rules

### 3.1 Slug (`name` field)

- Lowercase ASCII letters, ASCII digits, and the hyphen character only.
- Must start and end with a letter or digit. No leading or trailing hyphen.
- Length: 1 to 64 characters inclusive.
- Pattern (PCRE): `^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`

Identical to v1 `validCLIName`. Reuses the same validator.

### 3.2 Display name

- Any printable UTF-8.
- Length: 1 to 200 characters inclusive.
- Surfaces in `agentcookie discover` output and `agentcookie secret list` headers.
- Never used as a path segment.

### 3.3 Path traversal protection

- `name` MUST NOT contain `..` segments. The slug rule above already prevents this, but the parser explicitly rejects `..` substrings as a defense-in-depth check.
- `[secrets.file].path` MUST NOT contain `..` segments.
- `[secrets.file].path` MAY start with `~/` (expanded to the current user's home directory). Absolute paths not starting with `~/` are accepted but logged as a soft-warning ("absolute path outside home directory may not be portable").

## 4. Collision rules

### 4.1 Two explicit manifests with the same `name`

Hard error. Both manifests are rejected from the registry. The error message names both source paths so the user can resolve manually.

Rationale: silent-skip would let an attacker shadow a real project by dropping a same-named manifest. Hard error makes the conflict visible.

### 4.2 Explicit manifest collides with PP CLI auto-derivation

Explicit manifest wins. The derived entry is suffixed with `-pp` (e.g., a PP CLI named `tesla-pp-cli` colliding with an explicit `tesla-pp-cli.toml` would register the derived entry as `tesla-pp-cli-pp`). Both entries appear in the registry. A stderr warning describes the override.

Rationale: explicit author intent wins over generator intent, but the derived entry remains available so the user can compare.

### 4.3 Two PP CLI auto-derivations produce the same name

Should be impossible (the PP generator owns cli_name uniqueness). If it happens, first-by-path-sort wins; subsequent collisions get a 6-character sha256 suffix and a stderr warning.

## 5. Secrets source kinds

Exactly one `[secrets.*]` block is required per manifest. Declaring more than one is a hard error.

### 5.1 `[secrets.file]`

```toml
[secrets.file]
path = "~/.config/last30days/.env"
```

- The file at `path` is read fresh on every source-side push.
- File format: same v1 `secrets.env` grammar (strict dotenv subset, see v1 spec section 3).
- Mode is not validated by agentcookie; the consuming CLI is responsible for setting appropriate permissions on its own files.
- If the file does not exist at read time, the project is omitted from the push envelope. A single stderr warning per push (not per file).

### 5.2 `[secrets.command]` (reserved, not in v2.0)

Reserved schema slot. v2.0 parsers reject with "exec source not yet supported."

### 5.3 `[secrets.keychain]` (reserved, not in v2.0)

Reserved schema slot. v2.0 parsers reject with "keychain source not yet supported."

### 5.4 `[[files]]` carried-file items

The `[secrets.*]` block carries `KEY=VALUE` env pairs. Some secrets are not env-shaped: a multiline EC private-key PEM or a TOML `config.toml` cannot ride as a single `KEY=VALUE` value. The `[[files]]` array carries arbitrary files from source to sink, sealed in transit and at rest, and materializes them on the sink as mode-0600 files under `~/.agentcookie/`.

`[[files]]` is a NEW construct that COEXISTS with the single `[secrets.*]` block. It is not a second `[secrets.*]` block, so the "exactly one `[secrets.*]`" rule (section 5) is unaffected. A manifest may declare zero or more `[[files]]` items alongside its one `[secrets.*]` block.

```toml
[[files]]
source = "~/.config/tesla-pp-cli/config.toml"   # required; file to read + base64 into the bus. ~/ expands.
key = "TESLA_CONFIG_TOML"                        # required; the wire key the base64 payload rides under.
target = "tesla-pp-cli/config.toml"              # required; materialization path, RELATIVE, under ~/.agentcookie/.
optional = false                                 # optional; default false. true = opt-in (see below).

[[files]]
source = "~/.tesla/fleet-private.pem"
key = "TESLA_FLEET_KEY_PEM"
target = "tesla-pp-cli/fleet-key.pem"
optional = true                                  # opt-in: NOT carried unless the user enables it.
```

#### Carriage mechanism

Because the wire envelope is a flat `map[string]map[string]string` (per-CLI -> key -> value), a carried file rides as a SINGLE key whose value is the **base64** encoding of the file bytes (single-line, dotenv-safe). Multiline values are never put on the wire. Each item adds two keys to its CLI's env map:

- `key` -> the base64 payload of the file bytes.
- `_FILE_<key>` -> the relative `target`. This reserved companion key (underscore-prefixed, per the v1 dotenv grammar) is the materialization instruction, so the sink needs no copy of the manifest.

On the sink, the secrets writer decodes each `_FILE_<key>`/`key` pair, writes the decoded bytes 0600 to `target` under `~/.agentcookie/`, and strips both keys from the per-CLI `secrets.env` (a carried file does not also leak as an env var).

#### Validation

- `source` is required and MUST NOT contain `..`. `~/` expands to the user's home.
- `key` is required and MUST be a valid environment variable name (initial letter or underscore, then letters/digits/underscores). Duplicate `key` across items is a hard error.
- `target` is required, MUST be relative (not absolute), MUST NOT contain `..`, and MUST resolve to a path strictly inside `~/.agentcookie/`. The sink re-validates the target and refuses to write (rather than write insecurely) if it escapes the root, the base64 is malformed, or the decoded payload exceeds 256 KB.

#### Opt-in (`optional = true`)

An item with `optional = true` is opt-in: discovery does NOT carry it unless the user enables it. Enabled item keys live one-per-line in `~/.agentcookie/file-optin/<name>.keys` (blank lines and `#` comments ignored). A default item (`optional = false`) is always carried. This gates deliberate exposures (e.g. a command-signing key landing on a headless sink) behind explicit per-user consent.

#### Security posture ("sealed")

Carried files are encrypted in transit over the tailnet AND at rest in the bus (the existing v0.12 sealed-envelope path). The materialized file on the sink is a 0600 plaintext file owned by the sink user: on-sink protection is file permissions. Files are written only under `~/.agentcookie/`, never world/group-readable, never outside that directory.

## 6. Sync policy

Identical semantics to v1 `[sync]` table.

```toml
[sync]
default = true                                # default-ship every key in the source file

[sync.keys]
SETUP_COMPLETE = false                        # exclude specific keys
FROM_BROWSER = false
INCLUDE_SOURCES = false
```

- `default` omitted -> `true`.
- `[sync.keys]` per-key entries override the default.
- The sync policy is applied source-side before the envelope is built. The sink never sees excluded keys.
- `[sync.keys]` does not travel in the wire envelope; per v1 spec section 4.3, this is source-side filter intent.

## 7. PP CLI auto-detect adapter

The discovery loop synthesizes an in-memory v2 manifest from `.printing-press.json` files found at `~/printing-press/library/*/`. No file is written to disk; the manifest exists only for the duration of the source process.

### 7.1 Field mapping

| v2 manifest field | Derived from |
|-------------------|--------------|
| `schema_version` | Always `2` |
| `name` | `.printing-press.json` `cli_name` |
| `display_name` | `.printing-press.json` `display_name` |
| `description` | `.printing-press.json` `description` |
| `project_kind` | Always `"cli"` |
| `homepage` | Omitted (not present in PP metadata) |
| `[secrets.file].path` | `~/.config/<cli_name>/config.toml` (PP CLI canonical location per [PP audit](audits/2026-05-22-pp-cli-auth-inventory.md)) |
| `[sync.keys]` (per key) | For each `auth_env_var_specs[i]` entry: if `sensitive = true`, key is default-shipped; if `sensitive = false`, `[sync.keys].<name> = false` |

### 7.2 Override

A PP CLI may ship an explicit `agentcookie.toml` (recommended for tier-A integration). Section 4.2 governs the collision behavior. The explicit manifest wins; the derived entry is suffixed.

### 7.3 Adapter authority

The adapter never reads the actual secrets file. It only synthesizes a manifest pointing at where the secrets live. The read-in-place step at push time is identical to any other v2 manifest.

## 8. Discovery semantics

### 8.1 Startup

On source process startup, the discovery loop runs once to build the initial registry.

### 8.2 Live updates

In `agentcookie source --watch` mode, an fsnotify watcher monitors each well-known directory. Create, write, and rename events trigger a re-scan. The debounce window is 250ms (matches v1 secrets-bus watcher).

### 8.3 Soft validation at discovery

A single malformed manifest does not abort the loop. The faulty file is soft-skipped with a stderr message describing the failure (parse error, name validation failure, unknown source kind). All other manifests are still loaded.

### 8.4 Hard validation at explicit import

The `agentcookie secret import-from` command remains the v1 imperative path and continues to hard-fail on malformed input. Discovery is forgiving; explicit import is strict.

### 8.5 Registry visibility

The `agentcookie discover` command surfaces:
- Every project in the registry (slug, tier, source path, read-in-place path, key count, sync filter).
- Every skipped manifest with the skip reason.
- Every collision with the resolution applied.

This is the user's window into auto-discovery.

## 9. Wire envelope

No envelope-shape changes. The v1 `Secrets map[string]map[string]string` field carries the merged payload (v1 bus + read-in-place discovery results). Sinks running v0.13 accept v0.14 source pushes transparently.

Carried files (section 5.4) ride inside this same flat map as ordinary keys: the base64 payload under `key` and the relative target under the reserved companion `_FILE_<key>`. No new envelope field is introduced; a sink that does not understand `_FILE_*` companions simply persists them as env keys (forward-degradation), while a current sink materializes and strips them.

## 10. Read-in-place vs copy-to-bus

### 10.1 Read-in-place (v2 default)

When discovery finds a manifest with `[secrets.file]`, agentcookie reads that file on every push. The file is source of truth; nothing is mirrored into `~/.agentcookie/secrets/<name>/`.

Advantages:
- Token rotation in the project's own file ships to sink on the next push.
- No drift between the project's view of the world and the bus's view.
- Removing the manifest removes the project from sync without further cleanup.

### 10.2 Copy-to-bus (v1 imperative path)

The `agentcookie secret import-from <path> --as <name>` command from v1 continues to work. It copies values into `~/.agentcookie/secrets/<name>/secrets.env`. The discovery loop recognizes these directories as synthetic `legacy-v1` registry entries.

Used when:
- The project's file path is dynamic or computed at runtime.
- The user wants to ship a stable snapshot rather than live values.
- The user wants to edit values in the bus without touching the project file.

### 10.3 Collision: both modes have an entry for the same name

The v1 bus directory wins per-key. A read-in-place value is used only for keys that are not in the v1 bus directory. This preserves explicit user intent (the v1 bus directory only exists because someone ran `import-from`).

## 11. Trust model

### 11.1 Discovery does not trust manifests

Every manifest goes through:
- Schema validation (TOML parse, required fields).
- Name validation (slug rules, traversal protection).
- Path validation (`[secrets.file].path` traversal protection).
- Collision check.

Anything that fails any check is soft-skipped at discovery time.

### 11.2 Discovery does trust the filesystem

Agentcookie assumes the user (or an installer the user trusts) put the manifests where they are. No signature verification in v2.0.

### 11.3 Signature verification (deferred)

The `signed_by` field is reserved in v2.0 for v2.1 use. When implemented, manifests in `/usr/local/share/agentcookie/manifests/` will be verifiable against a small set of trusted publisher keys. v2.0 ignores the field.

## 12. Forward compatibility

- `schema_version = 2` is the only accepted value in this spec. Future v3 manifests will use `schema_version = 3` and v2-aware parsers will hard-reject with "schema version not supported; upgrade agentcookie."
- New top-level fields added in v2.1+ are warned-but-accepted by v2.0 parsers per section 2.5.
- New `[secrets.*]` source kinds added in v2.1+ are rejected by v2.0 parsers with "source kind X not supported by this agentcookie version."

## 13. Governance

The spec lives in this repository at `docs/spec-agentcookie-secrets-bus-v2-adoption.md`. Changes happen via PR to this file. The Go parser at `internal/secretsbus/manifest_v2.go` is the reference implementation and the tie-breaker for any ambiguity in this document.

Third parties implementing parsers in other languages should treat this document as authoritative; if behavior differs from the Go reference implementation, the spec is what determines correct behavior. File issues for spec ambiguities.

## 14. Examples

### 14.1 Skill (last30days)

Manifest at `~/.agentcookie/manifests/last30days.toml`:

```toml
schema_version = 2
name = "last30days"
display_name = "last30days"
description = "Brand intelligence skill"
project_kind = "skill"
homepage = "https://github.com/mvanhorn/last30days-skill"

[secrets.file]
path = "~/.config/last30days/.env"

[sync]
default = true

[sync.keys]
SETUP_COMPLETE = false
FROM_BROWSER = false
INCLUDE_SOURCES = false
```

### 14.2 PP CLI explicit (tesla-pp-cli, post-regeneration)

Manifest at `~/printing-press/library/tesla/agentcookie.toml` (auto-emitted by the printing-press generator):

```toml
# Generated by printing-press 4.x from research/tesla-merged-spec.yaml.
# Do not edit; rerun generation to update.
schema_version = 2
name = "tesla-pp-cli"
display_name = "Tesla"
description = "Every Tesla mobile-API feature, plus offline charging history"
project_kind = "cli"

[secrets.file]
path = "~/.config/tesla-pp-cli/config.toml"

[sync]
default = false

[sync.keys]
TESLA_AUTH_TOKEN = true
```

### 14.3 PP CLI auto-derived (any PP CLI without regeneration)

Same shape as 14.2 but synthesized in-memory from `.printing-press.json`. Never written to disk.

### 14.4 Arbitrary third-party CLI

Manifest at `~/.agentcookie/manifests/my-tool.toml`:

```toml
schema_version = 2
name = "my-tool"
display_name = "My Tool"
description = "Internal tool"
project_kind = "cli"

[secrets.file]
path = "~/.config/my-tool/auth.env"
```

## 15. Relationship to v1

| Concern | v1 (wire format) | v2 (adoption standard) |
|---------|------------------|------------------------|
| File location | `~/.agentcookie/secrets/<name>/secrets.env` | `~/.agentcookie/manifests/<name>.toml` (declarations); secrets read from wherever the manifest points |
| Format | KEY=VALUE dotenv | TOML manifest pointing at KEY=VALUE dotenv |
| Adoption flow | `agentcookie secret import-from` (imperative, user-driven) | Project drops manifest, agentcookie auto-discovers (declarative, author-driven) |
| Source of truth | The bus directory | The project's own file (read-in-place) |
| Wire envelope | `envelope.Secrets map[string]map[string]string` | Same |
| Sink behavior | Write per-CLI bus directory | Same |
| Default behavior | Empty bus; user adds | Empty registry; user installs manifest-shipping projects |

v1 is the wire format. v2 is the adoption mechanism. Both ship in agentcookie v0.14.0-beta.1. v1 imperative paths continue to work; v2 declarative paths are the new recommended default.
