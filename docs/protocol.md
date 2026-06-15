# agentcookie protocol v1

This document captures the wire format between source and sink so future clients (a Linux sink, a Chrome extension on the sink, a hosted relay) can interoperate.

## Layers, outside to inside

1. **Transport.** HTTP over a Tailscale tailnet. POST `/sync` on the sink machine carries the sealed payload as the request body with `Content-Type: application/octet-stream`.
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
