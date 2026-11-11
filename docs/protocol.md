# agentcookie protocol v1, v2 and experimental v3

## Negotiated v3 live destinations

Transport remains AES-256-GCM over the paired Tailscale channel. v3 adds
`source_browser`/family/profile metadata, destination selection, per-surface
snapshot completeness and storage IR v1; it does not change pairing or invent a
new unencrypted transport. Types and validators in `internal/protocol` are the
wire authority.

The source queries authenticated `/capabilities` and chooses v3 only when the
sink advertises it. Capability and `/status?source=<hostname>` responses are
MAC-bound to the request nonce; an unauthenticated response cannot authorize a
counter reseed. A legacy sink receives the compatible cookie lane, never a
Gecko storage tarball. Different sinks can negotiate different surfaces. v1/v2
remain accepted for existing clients; the section below records historical v1.

A v3 envelope carries `protocol_version: 3`, `source_hostname`, monotonic
transport `sequence`, `destination` when selecting a named account destination,
`cookies`, optional `storage`, and `snapshot`:

- `generation`: durable source generation, distinct from transport sequence.
- `surfaces`: `cookies`, `local_storage`, `indexed_db`, each with a `state` and
  optional `reason`. `complete` authorizes removing absent **owned** records;
  `incomplete` preserves absent records; `unsupported` does not touch that
  surface. An empty array without authority is not logout.
- `sites`: `shipped`, `held`, or `excluded`, including container identity/reason.
  Held ambiguity is not an empty snapshot. Sink policy remains final.
- `inventory`: `listed`, `unread`, `unreadable`; an unread origin prevents a
  falsely complete storage snapshot. `authoritative_empty` is explicit.

### Storage IR v1

`storage.version` is 1. `origins` are exact scheme/host/port records with container
identity, independent localStorage/IndexedDB statuses, and optional records.
Values are bounded tagged structured-clone graphs, not JSON approximations.
Strings and database/store/index names preserve UTF-16 code units, including
lone surrogates. Ordered maps/sets, binary values, object references and cycles
must round-trip without silently discarding types. Unsupported or over-cap
values are incomplete/excluded with diagnostics, not deleted on the sink.

Limits before allocation: graph depth 64, 100,000 nodes, 16 MiB per record,
10 MiB per localStorage value, 64 MiB per database, 128 MiB per surface. These
ceilings complement transport/frame limits; they do not replace validation.

IndexedDB schema changes use browser transactions. Creating a fresh database at
v1 is distinct from upgrading an existing database to V+1. Missing schema can
require that upgrade, and callers fixed to a lower version then get
`VersionError`. A same-version conflict, blocked upgrade or failed multi-store
transaction cannot be reported as applied. Sink-only stores/records are not
owned merely because a database appears in the source snapshot.

### Accepted, applied and recovery

A successful transport reply is structured: overall status, accepted generation,
sequence and per-destination status. Read `accepted`, `applied`, `pending`,
`failed`, `conflicts`, `cap_excluded`, `ready` and `repair_needed`; acceptance
means durable desired state, not a usable browser login. `/status` supports
polling when a destination is closed or work is pending.

Each destination persists desired state, ownership hashes and write-ahead
intents. Cookie writes are read back and value-verified. Deletes require both
ownership and a matching last-applied value hash. Ambiguous crash outcomes are
reconciled rather than accepted as success. Missing ownership history exposes
`repair-needed`; explicit repair cannot identify revoked records whose ledger
was lost. Required destinations must be ready before their first visible window.

Replay tracking separates source/destination namespaces while retaining a stable
legacy migration floor: v2→v3→v2 transitions and process restarts must not reopen
old envelopes. Source sequence allocation is `max(now, previous+1)`. Losing
source generation state needs authenticated sink reconciliation and reseeding,
not deleting sequence state or retrying an old logout generation.

The feature is experimental until the [release gates](release-gates.md) pass.
Do not infer real-browser validation from mock/protocol tests.

## Historical v1 format and legacy behavior

The remainder describes the original protocol, including explicitly historical
limits. Its version-equality and in-memory replay descriptions do **not** describe
the current negotiated v3 implementation.


This document captures the wire format between source and sink so future clients (a Linux sink, a Chrome extension on the sink, a hosted relay) can interoperate.

## Layers, outside to inside

1. **Transport.** HTTP over a Tailscale tailnet. The default path is POST `/sync` on the sink: the source pushes a sealed payload as the request body with `Content-Type: application/octet-stream`. Client-only sinks that cannot accept inbound HTTP (Muse-like sandboxes) instead poll the source: `GET /pull` on the source's watch listener (the pairing port, default `:9998`) returns the same sealed envelope. `/pull` is guarded by HMAC-SHA256 request auth over a timestamp and nonce using the existing peer key (`internal/transport`). Envelope crypto is unchanged.
2. **Authenticated encryption.** AES-256-GCM. The key is derived from the source-sink pair via X25519 + HKDF-SHA256 (see `internal/pairing`), or from the legacy `security.shared_secret` YAML field. Each message carries a fresh 12-byte nonce as the first 12 bytes of the ciphertext; the GCM tag is appended automatically. Wrong-secret payloads are rejected by the AEAD tag check.
3. **Envelope.** Inside the seal, the plaintext is a JSON `SyncEnvelope`.

## SyncEnvelope (JSON)

```
{
  "protocol_version": 1,
  "source_hostname": "my-laptop.tailnet.ts.net",
  "sequence": 1747432817,
  "cookies": [ { ... }, { ... } ]
}
```

Field-by-field:

- `protocol_version` (int, required). Sinks reject envelopes whose value does not equal the sink's compiled-in version. Bumping the version is a breaking change.
- `source_hostname` (string, required). The source's announced hostname. The sink uses this as the key in its replay-defense bookkeeping. The same hostname appears in the paired key file under `~/.config/agentcookie/keys/`.
- `sequence` (int64, required). Monotonically increasing per source. Sinks reject an envelope whose `sequence` is less than or equal to the highest sequence seen for that source since process start. The source uses `time.Now().Unix()` by default; any monotonically increasing source is acceptable.
- `cookies` (array, required). Each cookie matches the `chrome.Cookie` Go struct: `host_key`, `name`, `value` (plaintext after source-side decrypt), `path`, `expires_utc`, `is_secure`, `is_httponly`, `last_access_utc`, `has_expires`, `is_persistent`, `priority`, `samesite`, `source_scheme`, `source_port`. Values are integers and strings, never raw bytes.

## Sink validation order

1. Decrypt the body with the configured shared/paired key. Reject `401 Unauthorized` on failure.
2. JSON-unmarshal the envelope. Reject `400 Bad Request` on failure.
3. Check `protocol_version == 1`. Reject `400` with a clear message otherwise.
4. Check `sequence` against the in-memory tracker for `source_hostname`. Reject `409 Conflict` if not strictly greater than the last seen value.
5. Filter `cookies` against the sink's local cookie policy in `blocklist.yaml`. In blocklist mode, matching hosts are dropped. In allowlist mode, only matching hosts are kept. The sink logs dropped host counts and reports them to the source via the response body.
6. Write the remaining cookies. CDP path first if `cdp.enabled` and Chrome reachable; otherwise SQLite path.

## Replay defense (v0.1 limits)

The sequence tracker is in-memory only. Restarting the sink clears it; a captured payload could be replayed once after a restart. Durable replay defense is a v0.2 follow-up; the envelope is shaped so it can absorb a timestamp window or a server-issued nonce without breaking the version.

## Cookie policy (sink side, defense in depth)

The sink reads `~/.config/agentcookie/blocklist.yaml` independently of the source. If the source pushes a cookie for a host the sink policy rejects, the sink drops it. This means the sink owner has the final say on what state may land in their Chrome, even if the paired source is fully compromised. The source-side policy still runs first as a bandwidth and privacy optimization (no point sealing cookies that will be dropped).

Default blocklist schema:

```yaml
version: 1
domains:
  - pattern: "instacart.com"
    description: Instacart exact host
  - pattern: "%.instacart.com"
    description: Instacart subdomains
```

Omitted `policy` means `blocklist`, preserving the v0.3 sync-all default when
the file is missing or `domains` is empty. `policy: blocklist` is accepted and
behaves the same way.

Explicit allowlist schema:

```yaml
version: 1
policy: allowlist
domains:
  - pattern: "github.com"
    description: GitHub exact host
  - pattern: "%.github.com"
    description: GitHub subdomains
```

Allowlist mode is for high-trust/headless agent deployments where only named
sessions should leave the source machine. Only matching `host_key` patterns
sync; all other cookie hosts are dropped on the source and again on the sink.
An empty allowlist syncs no cookie hosts and is reported by `agentcookie doctor`.

Patterns use SQLite LIKE semantics (`%` wildcard). Matching is case-insensitive and matches against Chrome's `host_key` column, which includes a leading dot for subdomain cookies. Prefer `agentcookie accounts off <domain>` for normal site toggles; it writes the exact host plus a subdomain-safe `%.` pattern.

A missing `blocklist.yaml` still means sync-all; a present but unparseable policy intentionally halts sync instead of falling back to sync-all.

## Versioning

- v1 is the current wire format. Source and sink must both speak it.
- Future versions: bump `protocol_version` and update both source and sink. Sinks may carry compatibility shims for one prior version during transition windows.
- Out-of-band fields (e.g. filter sync, signed bundles, cookie diffs) will arrive as additional optional envelope fields under v1 first, then potentially graduate to required in v2.
