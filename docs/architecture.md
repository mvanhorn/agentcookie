# agentcookie architecture

## The picture

```
        SOURCE (laptop)                                  SINK (Mac mini / cloud VM)
   +---------------------------+                    +-----------------------------+
   |  Chrome stable            |                    |  Chrome stable              |
   |    Cookies SQLite         |                    |    Cookies SQLite           |
   |    Safe Storage (Keychain)|                    |    Safe Storage (Keychain)  |
   |                           |                    |                             |
   |  agentcookie source       |                    |  agentcookie sink (launchd) |
   |    - read SQLite (RO)     |                    |    - listen :9999/sync      |
   |    - decrypt w/ local key |     AES-GCM        |    - decrypt seal           |
   |    - filter by allowlist  |    over HTTP       |    - check proto + seq      |
   |    - wrap in envelope     | ================>  |    - filter by allowlist    |
   |    - seal w/ peer key     |    on tailnet      |    - write Chrome SQLite    |
   |                           |    (WireGuard)     |    - write sealed sidecar   |
   +---------------------------+                    +-----------------------------+
            ^                                                  ^
            |                                                  |
            +-----------  Tailscale tailnet  ------------------+
            |             (WireGuard, ACLs)                    |
            +--------------------------------------------------+
```

## Module layout

| Package | Purpose |
|---------|---------|
| `cmd/agentcookie` | CLI entry point (cobra). |
| `internal/cli` | Subcommand implementations: `source`, `sink`, `pair`, `status`, `version`. |
| `internal/chrome` | Read + decrypt Chrome cookies on macOS via Keychain Safe Storage + SQLite. Schema-aware INSERT for the write path. |
| `internal/transport` | AES-GCM seal/open with key = SHA-256(secret). |
| `internal/config` | YAML loaders for `source.yaml`, `sink.yaml`, `allowlist.yaml`. Tilde expansion, defaults, validation. |
| `internal/pairing` | X25519 + HKDF handshake. Source listens for pairing; sink connects with the printed code. Both sides derive identical 32-byte keys. |
| `internal/keystore` | Per-peer key files at `~/.config/agentcookie/keys/<peer>.json` mode 0600. |
| `internal/protocol` | `SyncEnvelope` (versioned), `SequenceTracker` (in-memory replay defense), `AllowlistMatcher` (SQLite-LIKE patterns, case-insensitive). |
| `internal/cdp` | Tiny Chrome DevTools Protocol client: `Probe` + `Dial` + `Call`. One method we care about: `Storage.setCookies`. |

## Lifecycle: one sync

1. `agentcookie source --once` runs on the laptop.
2. Reads `~/.config/agentcookie/source.yaml` for sink URL and `peer.hostname`.
3. Reads `~/.config/agentcookie/allowlist.yaml` for domain patterns.
4. Loads the paired key for `peer.hostname` from `~/.config/agentcookie/keys/`.
5. Calls `security find-generic-password` to get Chrome Safe Storage; derives the per-machine AES key.
6. Opens Chrome's Cookies SQLite read-only with `immutable=1`. Selects rows matching each allowlist pattern.
7. Decrypts each `encrypted_value` (v10 prefix, AES-128-CBC, IV = 16 spaces, PKCS#7).
8. Wraps the cookies in a `SyncEnvelope` with version, hostname, monotonic Sequence.
9. AES-GCM-seals the envelope with the paired key.
10. POSTs to the sink's `/sync` URL.

On the sink, in the `/sync` handler:

1. Reads the raw bytes.
2. Loads the paired key for the configured source hostname; AES-GCM-opens the payload. Wrong key -> 401.
3. JSON-unmarshals the `SyncEnvelope`.
4. Checks `ProtocolVersion == 1`. Mismatch -> 400.
5. Checks `Sequence` against the in-memory `SequenceTracker`. Replay -> 409.
6. Filters cookies against the sink's own `allowlist.yaml`. Dropped hosts are counted for logging.
7. If `cdp.enabled`, probes `http://<host>:<port>/json/version`, dials the browser-level WebSocket, sends `Storage.setCookies`. On any failure, falls back to step 8.
8. Otherwise opens the sink's Cookies SQLite read-write, re-encrypts each value with the SINK's Chrome Safe Storage key, upserts rows via a schema-aware INSERT ... ON CONFLICT (handles Chrome's `top_frame_site_key`, `source_type`, `has_cross_site_ancestor` columns dynamically).

## Lifecycle: pairing

1. Source: `agentcookie pair --as source` generates an X25519 ephemeral keypair and a fresh base32 code (e.g. `YILU-OIVK`). Listens on `:9998/pair`. Prints the code and the sink-run command.
2. Sink: `agentcookie pair --as sink --peer <source-host> --pair-url ... --code YILU-OIVK` generates its own X25519 keypair, POSTs `(code, sink_pub, sink_hostname)` to source.
3. Source checks the code (constant-time compare). Computes `shared = X25519(source_priv, sink_pub)`. Derives `key = HKDF-SHA256(shared, salt=code, info="agentcookie-pair-v1")[:32]`. Replies with `(source_pub, source_hostname, fingerprint)`.
4. Sink computes the same `shared`, derives the same key. Verifies the source's fingerprint matches its own. Writes the key to `~/.config/agentcookie/keys/<source-host>.json` mode 0600.
5. Source's listener shuts down; the key it derived is also written to disk on the source side, keyed by the sink's hostname.

## Where the security boundaries are

| Boundary | Enforced by |
|----------|------------|
| OS user separation on source and sink | macOS user accounts, file mode 0600 on key files and configs |
| Cookie value at rest | Chrome Safe Storage per-machine AES key + Keychain access prompt |
| Cookie value in transit | AES-GCM with paired key + Tailscale WireGuard channel |
| Pairing authenticity | Pairing code mixed into HKDF salt; MITM derives different key |
| Sink-side opt-in | `allowlist.yaml` on sink; cookies for non-allowlisted hosts are dropped before writing |
| Replay defense | `SequenceTracker` in sink memory; rejects equal-or-lower Sequence |
| Protocol stability | `ProtocolVersion` int in every envelope; breaking changes bump the number |

See [threat-model.md](threat-model.md) for what each of these protects against and what they don't.
