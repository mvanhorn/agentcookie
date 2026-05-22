---
title: agentcookie secrets bus format specification
schema_version: 1
status: v1 draft
created: 2026-05-22
---

# agentcookie secrets bus v1

This document specifies the on-disk and transport-visible format of the agentcookie secrets bus. The format is a public contract. An external author should be able to read this document and write a conforming reader in any language without consulting agentcookie source.

The two roles named in this document are:

- **The agentcookie source.** The actor that owns the laptop side of the bus. It observes the laptop filesystem, applies sync policy, and ships secrets through the agentcookie transport to the sink.
- **The agentcookie sink.** The actor that owns the sink-machine side of the bus. It receives payloads from the source and materializes them at the standard paths on the sink filesystem.

A consumer is any CLI (or other program) that reads from the bus at runtime. Consumers see only the on-disk file shape described below. They do not need to participate in the transport protocol.

## 1. Scope and non-scope

### 1.1 What this format is

- A transport-visible and on-disk shape for per-CLI secrets.
- A directory layout under a single well-known root, addressable by CLI name.
- A line-oriented value file in the `.env` family that any mainstream dotenv parser can consume.
- A small TOML manifest that describes the per-CLI dataset and carries sync-policy hints.
- An optional sealed-twin file for at-rest encryption on the sink. The sealed twin is opaque to consumers; the only public-visible properties are its filename and the rule that it shadows its plaintext sibling.

### 1.2 What this format is not

- It is not a secret store. agentcookie does not generate secrets, prompt for new credentials, or surface a vault interface. It moves and stores values that a CLI already has.
- It is not a credential issuer. Logging in, completing OAuth, minting API keys, and refresh-token rotation are entirely the CLI's responsibility. The bus carries the result.
- It is not a rotation system. If a secret expires or is revoked, the next write from the authoritative side propagates the replacement. The bus has no concept of expiry, validity windows, or revocation lists.
- It is not a remote API. There is no network surface defined here. The transport that moves payloads between source and sink is specified in `docs/protocol.md`; this document covers only the file shape carried inside that transport and materialized on each side.
- It is not a key-management protocol. The sealed twin reuses the existing agentcookie at-rest sealing layer; this document does not redefine that layer.

## 2. Directory layout

### 2.1 Root path

All secrets live under a single root directory:

```
~/.agentcookie/secrets/
```

The root is owned by the user that runs agentcookie. The root directory itself has mode `0700`.

### 2.2 Per-CLI subdirectory

Each consumer that participates in the bus has its own subdirectory directly under the root:

```
~/.agentcookie/secrets/<cli-name>/
```

The subdirectory has mode `0700`.

### 2.3 CLI name rules

The `<cli-name>` segment is a stable identifier chosen by the consumer's author. It MUST follow these rules:

- Lowercase only. The set of permitted characters is `a` through `z`, `0` through `9`, and the ASCII hyphen `-`.
- It MUST NOT begin or end with a hyphen.
- It MUST NOT contain two consecutive hyphens.
- It MUST NOT contain a dot, a slash, a backslash, whitespace, or any other punctuation.
- It MUST NOT be `.` or `..` or any other path-traversal token.
- Length is at least one character and at most sixty-four characters.

A reader that encounters a path component that fails any of these rules MUST refuse to open the directory and MUST report an error naming the violating component. Readers MUST NOT silently normalize names (no case folding, no underscore-to-hyphen rewriting).

The agentcookie sink applies these same rules before materializing a payload. A payload that names an invalid CLI is rejected; the sink logs the rejection and writes nothing outside the secrets root.

### 2.4 Files inside a per-CLI directory

A conforming per-CLI directory contains the following files. All of them are optional except where noted; a directory with only a manifest and no value file is legal and represents a registered consumer with no current secrets.

| Filename               | Required | Contents                                                                                                                                                |
| ---------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `secrets.env`          | No       | Plaintext line-oriented `KEY=VALUE` pairs. Defined in section 3.                                                                                       |
| `manifest.toml`        | Yes      | TOML metadata describing the dataset and sync policy. Defined in section 4.                                                                            |
| `secrets.env.sealed`   | No       | Opaque sealed twin of `secrets.env`. Defined in section 5.                                                                                             |

Any other file in the per-CLI directory is ignored by conforming readers. The bus does not reserve names for future use beyond the three listed above; new file kinds will be introduced through a manifest schema bump (section 10).

### 2.5 No nesting

The per-CLI directory has no subdirectories. A reader MUST NOT descend into nested directories under a CLI name. If one is present, it is ignored.

## 3. The `secrets.env` line format

`secrets.env` is a plaintext UTF-8 file. Each non-empty, non-comment line carries one `KEY=VALUE` pair. The grammar below is intentionally a strict subset of the broader "dotenv" family, so that values written by one mainstream parser are read back identically by any other.

### 3.1 Grammar

In ABNF-flavored form:

```
file        = *line
line        = blank / comment / entry
blank       = *WSP NEWLINE
comment     = *WSP "#" *VCHAR NEWLINE
entry       = key "=" value NEWLINE
key         = ALPHA / "_" *( ALPHA / DIGIT / "_" )
value       = bare-value / dq-value / sq-value
bare-value  = *( safe-char / "\" continuation )
safe-char   = %x21-22 / %x24-3D / %x3F-5B / %x5D-7E   ; printable, no whitespace, no #, no =, no \, no "
dq-value    = DQUOTE *( dq-char / "\" dq-escape ) DQUOTE
dq-char     = %x20-21 / %x23-5B / %x5D-7E             ; printable except " and \
dq-escape   = DQUOTE / "\" / "n" / "r" / "t"
sq-value    = SQUOTE *sq-char SQUOTE
sq-char     = %x20-26 / %x28-7E                       ; printable except '
continuation= NEWLINE                                 ; only when last char of line is "\"
```

Plain English summary:

- Lines that start with optional whitespace and then `#` are comments. Comments occupy the whole line; trailing comments after a value are not supported.
- Blank lines are allowed and have no effect.
- A key starts with an ASCII letter or underscore and continues with letters, digits, or underscores. Keys are case-sensitive.
- The single `=` between key and value is required. There is no whitespace allowed around the `=`. `KEY = value` is invalid; the equals sign must be flush.
- A value is one of three shapes: bare, double-quoted, or single-quoted.
  - A bare value runs from immediately after the `=` to the end of the line. It MUST NOT contain whitespace, `#`, `=`, `\`, or `"`.
  - A double-quoted value supports the escape sequences `\"`, `\\`, `\n`, `\r`, `\t`. No other backslash escape is defined.
  - A single-quoted value is taken verbatim. No escape sequences are recognized; the value continues until the next `'`.
- Multi-line values are supported only via backslash continuation. A backslash as the last character of a line joins the next line into the current value. The backslash and the newline are both removed; the next line's leading whitespace, if any, is preserved.
- The file MUST be UTF-8. A byte order mark (BOM) at the start of the file is permitted and ignored by conforming readers.

### 3.2 Explicit forbiddens

The following dotenv-family features are explicitly excluded. Writers MUST NOT emit them. Readers MUST treat them as syntax errors.

- Variable interpolation. `KEY=$OTHER` is a literal value `$OTHER`, not a reference. There is no `${OTHER}` form.
- Command substitution. `KEY=$(cmd)` is a literal value.
- `export KEY=...` prefix. The bare `export` keyword is not recognized; a line that begins with it is a syntax error.
- Bare JSON, YAML, TOML, or other structured values without explicit string quoting. A value that begins with `{` or `[` is permitted only when it appears inside double quotes or single quotes.
- Heredoc or triple-quoted block syntax. The only multi-line mechanism is backslash continuation.
- Trailing comments on the same line as a value. `KEY=foo # comment` parses `foo # comment` as the value.

Excluding these features keeps the format portable across the major dotenv libraries (Go `joho/godotenv`, Python `python-dotenv`, Node `dotenv`, Ruby `dotenv`) without depending on any parser-specific extension.

### 3.3 Valid examples

```
# OAuth bearer with a long opaque value.
TESLA_OAUTH_BEARER=eyJraWQiOiI4Y0w1RXVqaXN6dmJrUm9PUEFlSzNNYW1kRmM4dG5oVDB6
```

```
# Quoted value, because the value contains a hash character.
SUNO_SESSION_TAG="abc#123"
```

```
# Single-quoted value preserves a literal dollar sign.
GOAT_NOTE='take $5 off next order'
```

```
# Backslash continuation joins two lines into one logical value.
SUPERHUMAN_REFRESH=eyJhbGciOi...firstpart...\
secondpart...AwIA
```

```
# Empty value is legal. Useful for "key exists but is intentionally blank".
EBAY_ACCOUNT_TAG=
```

### 3.4 Invalid examples

```
# Whitespace around = is not allowed.
GH_TOKEN = ghp_xxx
```

Reason: the grammar requires the `=` to be flush against both key and value. A reader MUST reject this line and report the line number.

```
# Trailing comment on a value line is not allowed.
LINEAR_TOKEN=lin_api_xxx # leave room to rotate
```

Reason: comments occupy the whole line. The value here parses as `lin_api_xxx # leave room to rotate`, which is almost never what the author intended. To avoid silent capture of comment-shaped suffixes, conforming readers MUST treat the resulting value as legal but writers SHOULD NOT emit values that look like a trailing comment. (This particular case is a SHOULD-NOT for writers, MUST-accept for readers; the line is grammatically valid because `#` is a `safe-char` inside `bare-value`.)

```
# Variable interpolation is not honored.
DERIVED_TOKEN=$BASE_TOKEN
```

Reason: `$BASE_TOKEN` is a literal value. A reader returns the string `$BASE_TOKEN`, not the resolved value of `BASE_TOKEN`. This is legal and parses cleanly; it is included here as an "invalid intent" example. Writers that meant to reference another key MUST resolve the value before writing.

```
# Heredoc syntax is not part of this grammar.
MULTI<<EOF
line one
line two
EOF
```

Reason: the only multi-line mechanism is backslash continuation. The first line is a syntax error because `MULTI<<EOF` does not contain a flush `=`.

## 4. The `manifest.toml` schema

`manifest.toml` is a TOML 1.0 document. It describes the per-CLI dataset and carries the sync-policy hints that the agentcookie source consults before transmitting a payload.

### 4.1 Required fields

```
schema_version = 1
display_name = "Tesla PP CLI"
```

- `schema_version` (integer, required). The version of this manifest schema. The current value is `1`. Writers MUST set it; readers MUST inspect it (see section 10).
- `display_name` (string, required). A human-readable label for the consumer. Used in `agentcookie secret list`, doctor output, and any future UI. UTF-8, between one and one hundred and twenty-eight characters.

### 4.2 Optional `[sync]` table

```
[sync]
default = true
```

- `default` (boolean, optional, default `true`). The default sync policy for the dataset. When `true`, every key in `secrets.env` is shipped to the sink unless overridden in `[sync.keys]`. When `false`, no key is shipped unless explicitly overridden to `true` in `[sync.keys]`.

When `[sync]` is absent entirely, the default is equivalent to `[sync] default = true`.

### 4.3 Optional `[sync.keys]` table

```
[sync.keys]
TESLA_SNOWFLAKE_PRIVATE_KEY_PEM = false
TESLA_FLEET_PARTNER_TOKEN = true
```

`[sync.keys]` is a flat TOML table whose keys are the same names that appear in `secrets.env`, and whose values are booleans.

- A key set to `true` is always shipped to the sink, regardless of `[sync] default`.
- A key set to `false` is never shipped to the sink, regardless of `[sync] default`.
- A key absent from `[sync.keys]` inherits `[sync] default`.

Keys listed in `[sync.keys]` that do not appear in `secrets.env` are ignored (forward-compatible: a manifest can pre-declare policy for keys a future version of the consumer will write).

### 4.4 Reserved fields

The `[meta]` table and any field whose name begins with an underscore (`_*`) are reserved for future agentcookie use. Writers SHOULD NOT set them; readers MUST ignore them. See section 8.

### 4.5 A fully worked manifest example

Consider a hypothetical consumer named `example-pp-cli` whose secrets include a long-lived API key (safe to sync), a per-machine OAuth refresh token that each side mints on its own (must not be overwritten by the source), and a binary signing key that exists only on the laptop:

```
schema_version = 1
display_name = "Example PP CLI"

[sync]
# Most keys are safe to ship to the sink.
default = true

[sync.keys]
# This refresh token is rotated independently on each machine.
# Sink must keep its own copy, never receive the source's value.
EXAMPLE_OAUTH_REFRESH = false

# This signing key is bound to the laptop's secure enclave.
# It does not leave the laptop under any circumstance.
_BIN_EXAMPLE_SIGNING_PRIVATE_KEY = false
```

Effect on the wire payload: the agentcookie source reads `secrets.env`, drops the two keys with `sync = false`, and ships only the remaining keys to the sink.

## 5. The sealed-twin file `secrets.env.sealed`

### 5.1 Purpose and visibility

`secrets.env.sealed` is an at-rest encryption of the same key/value content that `secrets.env` would carry. Its body is opaque to consumers and to readers in other languages. The format internals are owned by agentcookie and may evolve without bumping the schema version of this document, because no consumer needs to interpret the bytes directly.

What this specification defines about the sealed twin:

- The filename. It is always `secrets.env.sealed` in the same per-CLI directory as the plaintext sibling. No other suffix is recognized.
- When it appears. The sealed twin is present whenever the agentcookie sink (or, optionally, the source) has been configured to keep at-rest sealing on for that machine.
- How consumers detect and use it. Defined in section 5.2.

### 5.2 Reader behavior in the presence of a sealed twin

A reader that supports sealed values MUST resolve a per-CLI dataset by checking, in this order:

1. `secrets.env.sealed`. If present, the reader decrypts it (via the agentcookie reader library or by shelling out to `agentcookie secret get`). On success, the resulting key/value map is the dataset. The plaintext sibling, if it also exists, is ignored.
2. `secrets.env`. If the sealed twin is absent (or sealing is not configured on this machine), the reader parses the plaintext file directly.

A reader that does not support sealed values (for example, a third-party CLI loaded purely via its native dotenv parser) sees only `secrets.env`. On a sealed-only sink, that consumer must either acquire sealing support (via the agentcookie reader library) or be invoked through a small shim that calls `agentcookie secret get`.

Conforming writers MUST NOT leave both files present in a state where they disagree. The agentcookie sink, when sealing is on, writes the sealed twin first and then removes the plaintext file in the same atomic-rename window. Writers that produce only plaintext MUST NOT touch an existing sealed twin.

### 5.3 What consumers MUST NOT assume about the sealed twin

Consumers MUST NOT:

- Attempt to read the sealed file with a dotenv parser. The body is not parseable as `KEY=VALUE`.
- Assume that the presence of `secrets.env.sealed` implies the absence of `secrets.env`. During a brief atomic-rename window the two may coexist; the order in section 5.2 makes that case unambiguous.
- Depend on any specific size, header, or magic bytes in the sealed file.

## 6. File modes and atomic writes

### 6.1 Mode

Every file under `~/.agentcookie/secrets/` is mode `0600`. Every directory under it is mode `0700`. Writers MUST set these modes at creation time, MUST NOT widen them later, and SHOULD verify them on open.

A reader that encounters a file with mode other than `0600` SHOULD log a warning. It MUST NOT refuse to read on that basis alone (a misconfigured umask is a common cause and the value may still be authoritative), but the warning surfaces the misconfiguration.

### 6.2 Atomic writes

All writes to files in the bus MUST be atomic with respect to readers running concurrently. The standard recipe:

1. Write the new content to a temporary file in the same directory, named `<final-name>.tmp.<random-suffix>`. The temporary file is created with mode `0600`.
2. `fsync` the temporary file.
3. `rename` the temporary file over the final name.
4. (Recommended) `fsync` the containing directory.

Readers that follow the section 5.2 priority chain will see either the previous value or the new value, never a torn read. Writers that fail to follow this discipline are non-conforming.

### 6.3 Concurrent writers

The bus assumes a single writer at a time per per-CLI directory. The two writers in practice are the agentcookie sink (during a `/sync`) and `agentcookie secret set` invoked manually. These two paths coordinate via the same atomic-rename discipline; concurrent invocations are race-safe at the filesystem level but a value written by one may be replaced moments later by the other. There is no locking protocol beyond atomic rename.

## 7. Reserved key names

The bus reserves a small prefix space for agentcookie-internal markers. Consumer code MUST tolerate seeing these keys in the map returned by a reader, even if it ignores them.

### 7.1 Single leading underscore

Any key in `secrets.env` whose name begins with a single underscore is reserved. The current reservations:

- `_unknown_<field>`. Written by `agentcookie secret import-from` when it cannot map a field name from a source file (TOML, JSON, etc.) onto a known canonical key. The value is the original field's value verbatim. The friend is expected to inspect, rename, and remove the `_unknown_` prefix. Conforming readers SHOULD surface these to the consumer as ordinary entries; consumers SHOULD treat them as a signal that the import is incomplete.
- `_BIN_<KEY>`. Marks a value that was originally binary (raw bytes that did not survive UTF-8 encoding intact). The value is base64-encoded with standard alphabet and no line wrapping. Consumers that recognize the prefix MAY decode the value; consumers that do not recognize it see a base64 string. The original key name is the suffix after `_BIN_`. The marker exists because the `.env` grammar (section 3) forbids non-printable bytes in values, and binary signing keys, certificates, and similar are common enough to need a defined fallback.
- `_meta_<field>`. Reserved for future agentcookie metadata that travels with the value file (last-import-source, last-import-time, etc.). v1 does not define any concrete `_meta_*` names.

### 7.2 Double leading underscore

Keys beginning with `__` are reserved for future use. Writers MUST NOT emit them. Readers MUST ignore them silently.

### 7.3 Non-reserved underscored names

A single underscore inside a key (e.g. `MY_API_KEY`) carries no special meaning and is unaffected by these reservations. Only the leading character pattern is significant.

## 8. Security boundary

This section is a precise statement of what the format is designed to protect and what it deliberately leaves to other layers.

### 8.1 What the format protects against

- **Over-the-wire interception of secrets between source and sink.** The transport (specified in `docs/protocol.md`) wraps the payload in an authenticated-encryption envelope keyed by a paired secret. A passive observer on the network between the two machines cannot recover any secret values from the wire.
- **Opportunistic local reads on the sink when sealed mode is enabled.** When the sink writes a `secrets.env.sealed` twin instead of a plaintext file, a process running as the user but without access to the at-rest sealing key cannot recover values from the file alone.
- **Cross-CLI bleed.** Each consumer has its own directory and its own value file. A consumer that reads `Load("foo-pp-cli")` cannot accidentally observe `bar-pp-cli`'s secrets through this format. (Filesystem-level read permission on `~/.agentcookie/secrets/` is the same for all consumers running as the user; the directory layout enforces a logical, not adversarial, boundary.)
- **Sync of secrets the friend marked local-only.** The `[sync.keys]` `false` policy keeps a key off the wire entirely. A laptop-only signing key, marked `sync = false`, is never shipped to the sink under any payload.

### 8.2 What the format does not protect against

- **Root user on the machine.** A process running as root (or, on macOS, as the same user with full disk access) can read `~/.agentcookie/secrets/` directly. The bus relies on filesystem permissions for in-user-session boundaries; it offers no defense against privilege escalation.
- **Disk-level encryption disabled.** When the host disk is unencrypted (no FileVault on macOS, no LUKS on Linux), the plaintext `secrets.env` is recoverable from a stolen machine. Sealed mode adds a layer here but does not substitute for full-disk encryption.
- **Physical theft of an unlocked machine.** No file-format choice protects a logged-in session from an attacker with hands on the keyboard.
- **Compromise of the source.** A laptop that ships malicious payloads to the sink can write any secret value at any path the sink permits. The transport's allowlist + paired-key model is the relevant defense (see `docs/protocol.md`); this format trusts whatever the transport delivers.
- **Side-channel leakage by the consumer.** Once a consumer reads a value, what it does with that value (logging, transmitting to its own backend, writing to an unencrypted file elsewhere) is entirely outside this format's scope.

## 9. Backward compatibility and the protected-extension contract

This document is `schema_version = 1`. The contract for future versions is described in section 10. For v1, the compatibility guarantees are:

- Files written by a v1-compliant writer are readable by every v1-compliant reader.
- A v1 reader that encounters an additional, unrecognized file in a per-CLI directory ignores it (section 2.4).
- A v1 reader that encounters an additional, unrecognized table or field in `manifest.toml` ignores it (section 4.4).
- A v1 reader does not attempt to repair non-conforming files (e.g. mode `0644`). It surfaces them as warnings.

## 10. Versioning policy

### 10.1 The `schema_version` field

The single `schema_version` integer in `manifest.toml` (section 4.1) is the version of the entire format described by this document, including the `.env` grammar and the directory layout, not just the manifest itself.

### 10.2 Reader behavior on a higher version

A reader compiled against `schema_version = N` that encounters a file with `schema_version = M` where `M > N` MUST behave as follows:

- It MUST NOT crash.
- It MUST NOT silently downgrade or rewrite the manifest.
- It SHOULD log a warning that includes the observed `schema_version` and the reader's known maximum.
- It MAY attempt to parse the file under v1 rules and return whatever portion is intelligible, on the assumption that future schemas are deliberately compatible with the v1 baseline. This is best-effort; a reader that prefers safety MAY instead return an error.

Either choice (best-effort parse or error) is conforming. A library SHOULD document which it does and SHOULD make the choice configurable.

### 10.3 Writer behavior

Writers stamp `schema_version` with the highest version they emit. They MUST NOT emit a higher version than they actually conform to.

### 10.4 Breaking-change policy

A breaking change to this document increments `schema_version`. Examples of breaking changes:

- Adding a new required field to `manifest.toml`.
- Changing the meaning of an existing key or table.
- Introducing a new file that consumers MUST read for correctness.

Non-breaking changes (new optional tables, new reserved-prefix key names, new `_meta_*` entries) do not require a version bump. They are added to a successor of this document at the same `schema_version = 1`.

A v2 of the format, when it ships, will define its own migration story including whether v1 readers can continue to operate against v2 manifests.

## 11. Reference reader behavior

A reference reader takes a CLI name and returns a string-to-string map of resolved keys. The reader applies the following priority chain when populating that map. Each step contributes keys; later steps fill in keys that earlier steps did not provide. Keys that earlier steps did provide are NOT overwritten by later steps.

### 11.1 The four sources, highest priority first

1. **Sealed file.** `~/.agentcookie/secrets/<cli-name>/secrets.env.sealed`. Resolved per section 5.2. Provides the canonical bus dataset when sealing is in use.
2. **Plaintext file.** `~/.agentcookie/secrets/<cli-name>/secrets.env`. The fallback bus dataset when no sealed twin is present.
3. **Caller-registered fallback file.** Some readers accept an optional second argument naming the consumer's pre-existing config file (for example, the CLI's own `config.toml` under `~/.config/<cli-name>/`). When provided and present, this file's recognized keys feed the map. The reader is free to apply a field-name heuristic mapping (e.g. `access_token` -> `<CLI>_OAUTH_BEARER`); the mapping is reader-defined and out of scope for this document.
4. **Process environment.** Environment variables that match the consumer's expected key names. This is the source of last resort. It exists so that adopting the bus does not break consumers whose users set env vars directly today.

### 11.2 Why bus wins over env

A user who has adopted the bus may still have a leftover env var from a previous workflow. If env were higher-priority, the bus would be silently ignored on every machine that still exports the old name, and the user would conclude that sync is broken. Putting bus above env makes the bus the authoritative source whenever it is populated; the env var is the fallback for machines that have not yet adopted the bus.

This is the single most important non-obvious rule in this document. Consumers MUST follow it.

### 11.3 Empty values

An empty value (e.g. `KEY=` with nothing after the equals sign) is a legal entry. It SHOULD be returned in the map as the empty string and SHOULD NOT be treated as if the key were absent. Treating an empty value as absent would cause the next-priority source to leak in, which inverts the bus-over-env rule.

### 11.4 Errors

A reader that encounters a syntax error in `secrets.env` MUST return an error that names the file path and the offending line number. It MUST NOT silently skip the line, because a value the consumer needs may follow it and the consumer will then operate on a half-populated map.

A reader that encounters an invalid CLI name (section 2.3) MUST return an error before touching the filesystem.

A reader that encounters an unreachable sealed file (sealed mode is in use but the at-rest sealing key is not available) MUST return an error naming the sealed file path. It MUST NOT silently fall back to the plaintext sibling on a sealed-mode machine, because the plaintext sibling on a sealed machine is normally not present, and silent fallback would mask a real misconfiguration.

## 12. Worked end-to-end example

This section runs a hypothetical consumer named `example-pp-cli` through the format end-to-end. The consumer needs three secrets:

- `EXAMPLE_API_KEY`. A long-lived API key. Safe to sync.
- `EXAMPLE_OAUTH_REFRESH`. An OAuth refresh token that the consumer rotates independently on each machine. Must not be overwritten by the source; therefore not synced.
- `EXAMPLE_SIGNING_PRIVATE_KEY`. A raw-bytes signing private key. Binary, so it travels under the `_BIN_*` reserved prefix. Must not leave the laptop.

### 12.1 On the laptop (source)

The friend completes whatever login flow `example-pp-cli` requires. They then either (a) point the consumer's own login flow at the bus directly so it writes the standard paths, or (b) invoke `agentcookie secret import-from` to translate the consumer's native config into the bus shape. Either way, the resulting per-CLI directory looks like this:

```
~/.agentcookie/secrets/example-pp-cli/
  manifest.toml
  secrets.env
```

`manifest.toml`:

```
schema_version = 1
display_name = "Example PP CLI"

[sync]
default = true

[sync.keys]
EXAMPLE_OAUTH_REFRESH = false
_BIN_EXAMPLE_SIGNING_PRIVATE_KEY = false
```

`secrets.env`:

```
# Long-lived API key. Safe to ship to any machine that runs the CLI.
EXAMPLE_API_KEY=ex_live_3f4a2c91d8e6b5a07c1f9e4b6d0a8c2e

# OAuth refresh token. Each machine rotates its own; do not overwrite.
EXAMPLE_OAUTH_REFRESH=eyJhbGciOi...laptop-version...AwIA

# Binary signing key, base64-encoded. Local-only.
_BIN_EXAMPLE_SIGNING_PRIVATE_KEY=MIIEvQIBADANBgkqhkiG9w0BAQEFAASC...
```

Both files are mode `0600`. The directory itself is mode `0700`.

### 12.2 What the agentcookie source ships

The source reads `manifest.toml` and applies the sync policy. For this dataset:

- `EXAMPLE_API_KEY` inherits `[sync] default = true`. Shipped.
- `EXAMPLE_OAUTH_REFRESH` is overridden to `false`. Dropped.
- `_BIN_EXAMPLE_SIGNING_PRIVATE_KEY` is overridden to `false`. Dropped.

The wire payload (inside the agentcookie transport's authenticated-encryption envelope) carries one consumer entry, `example-pp-cli`, with one key, `EXAMPLE_API_KEY`. The manifest's `[sync] default = true` is also carried; the `[sync.keys]` table is not (it is source-side policy, not sink-side state).

### 12.3 On the sink

The agentcookie sink receives the payload and materializes it at the same standard path:

```
~/.agentcookie/secrets/example-pp-cli/
  manifest.toml
  secrets.env          (or secrets.env.sealed if sealed mode is on)
```

`manifest.toml` on the sink:

```
schema_version = 1
display_name = "Example PP CLI"

[sync]
default = true
```

(The sink does not synthesize a `[sync.keys]` table on the receive side; the source's policy has already been applied.)

`secrets.env` on the sink:

```
EXAMPLE_API_KEY=ex_live_3f4a2c91d8e6b5a07c1f9e4b6d0a8c2e
```

The sink does NOT see `EXAMPLE_OAUTH_REFRESH` or `_BIN_EXAMPLE_SIGNING_PRIVATE_KEY`. If the consumer on the sink needs a refresh token, it will perform its own OAuth refresh on first call and write the result into the sink's own `secrets.env` (the consumer's adoption of the bus may include writing back to it, depending on the consumer's design; that is the consumer's choice, not a format requirement).

### 12.4 What the consumer sees at runtime

When `example-pp-cli` runs on the sink and calls a reference reader for `example-pp-cli`, the reader applies the section 11 priority chain:

1. Sealed file: if sealed mode is on, the sealed twin is decrypted and the dataset is `{ EXAMPLE_API_KEY: ex_live_... }`.
2. Plaintext file: if sealed mode is off, the plaintext file yields the same dataset.
3. Caller-registered fallback: if the consumer's reader was invoked with a fallback path, any keys not already present (in this case, `EXAMPLE_OAUTH_REFRESH` and `_BIN_EXAMPLE_SIGNING_PRIVATE_KEY`) may be filled in from the consumer's local config file on the sink.
4. Process environment: any remaining keys are populated from the process environment, if set.

The consumer ends up with a complete-enough map to operate: a synced API key (from the bus) and a machine-local refresh token (from its own config file or from a freshly minted OAuth flow). The binary signing key is absent on the sink, by design.

### 12.5 What changes when the friend rotates the API key

The friend rotates `EXAMPLE_API_KEY` on the laptop. Whatever workflow they use (the consumer's native CLI, or `agentcookie secret set example-pp-cli EXAMPLE_API_KEY`) writes the new value to `~/.agentcookie/secrets/example-pp-cli/secrets.env` via atomic rename. The agentcookie source observes the file change, applies sync policy again, and ships the new payload. The sink writes the new value at the same path on the sink, also via atomic rename. The consumer on the sink picks up the new value on its next read (or, if it uses a long-running daemon that watches for file changes, on the next reload event).

No additional action is required from the friend on the sink. That is the whole point of the bus.

## 13. Open questions

These are items that the implementation will need to settle and the spec will need to revisit. They are listed here for transparency.

- **Sealed-twin format internals.** This v1 spec deliberately treats the sealed file as opaque and delegates the format to the agentcookie at-rest sealing layer. If a future need arises for a non-agentcookie tool to produce or consume a sealed twin, the internals will need to be promoted into this document or into a sibling spec.
- **Per-key vs whole-file sealing.** v1 seals the entire `secrets.env`. A future version may want to seal individual values (so a partial dataset can be loaded without the at-rest key, while sensitive keys remain protected). The format above is shaped so a `_meta_sealed_keys` array could be added without breaking v1 readers, but the actual design is deferred.
- **Cross-machine identity for `[sync.keys]`.** The policy `EXAMPLE_OAUTH_REFRESH = false` is applied symmetrically: the source does not ship it, and the sink does not ship its own copy back to the source either. The format does not currently distinguish "do not leave this machine" from "do not arrive at this machine"; both cases are covered by `false`. If a future use case needs directional policy, it will be added as an explicit field rather than overloading the existing boolean.
