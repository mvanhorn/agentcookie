# agentcookie threat model

This document captures what agentcookie does and does not protect against. Read it before deploying anywhere you care about; the absence of a threat in this list is a statement of scope, not of safety.

## What agentcookie does

Continuously replicates Chrome session cookies for a user-allowlisted set of domains from one macOS machine (the **source**, where the user logs in) to another (the **sink**, where AI agents run). The replication is one-way, opt-in per domain, and authenticated end-to-end with a pairing-derived symmetric key. Past v0.11, the sink also seals its on-disk cookie copies (sidecar SQLite and per-CLI session files) under a sink-local master key whose Keychain ACL pins the agentcookie binary plus each registered adapter binary.

## Trust model

agentcookie trusts:

- The OS on both machines, including the kernel, the user account boundary, file mode 0600, and the Chrome process.
- macOS Keychain to store the Chrome Safe Storage password and the agentcookie master key. v0.12 onward: those Keychain items carry a per-binary `-T` ACL that names the Developer-ID-signed agentcookie sink binary plus each registered adapter binary. Any other user-uid process cannot read them silently.
- The Tailscale tailnet for transport-layer confidentiality and identity. agentcookie layers its own AES-256-GCM on top, but the tailnet's WireGuard channel is the transport.
- The user's local filesystem under `~/.config/agentcookie/`. Anyone with read access to that directory can read the paired keys file (still on-disk plaintext JSON in v0.12; planned to migrate to the Keychain in a follow-up).
- The Chrome stable channel's documented cookie storage behavior.

## What agentcookie protects against

- Plaintext cookies in transit. Every payload is AES-256-GCM-sealed with a per-pair key. The key never appears unencrypted on the wire.
- Plaintext cookies at rest on the sink. When the v0.12 `agentcookie-master` Keychain item is present, the sink's plaintext sidecar SQLite is stored as sealed envelopes per value, and each adapter session file's secret-bearing fields are sealed under the same key. A non-allowlisted user-uid process reading those files sees opaque envelopes, not cookie values.
- Plaintext access to Chrome's own cookie store on the sink. v0.12 replaces the v0.10 `-A` (any-app) Keychain ACL on the Chrome Safe Storage password with a `-T` per-binary list. Only the agentcookie sink and registered adapter binaries can decrypt Chrome's Cookies SQLite silently; everything else needs a user prompt.
- Online brute force of the pairing code. v0.12: 12 base32 characters (64 bits of entropy) and a per-IP token bucket capped at 5 attempts before a 429.
- MITM during pairing. X25519 + HKDF salted with the pairing code means an attacker who intercepts the channel without knowing the code derives a different key, and the next AEAD message fails its tag check.
- Replay of captured payloads across sink restarts. v0.12: the sequence tracker is persisted to `~/.agentcookie/sequence.json` and reloaded at sink boot.
- Source pushing non-allowlisted cookies. The sink reads its own `allowlist.yaml` and drops cookies whose `host_key` matches no pattern, even if the source pushes them.
- Source pushing cookies with control characters or path-traversal in name / value / host_key. v0.12 adds RFC 6265 token-character validation on name, control-char rejection on value, and a label-boundary suffix check that fixes the v0.11 unanchored match (e.g., `xopentable.com` no longer matched `opentable.com`).
- Wrong-secret / unauthenticated requests. Both legacy `security.shared_secret` (now floored at 32 bytes) and paired keys gate every `/sync` call; AEAD tag mismatch returns 401.
- DoS via slow-loris and oversize bodies on the sink and pair listeners. v0.12 sets ReadHeaderTimeout (5s), ReadTimeout (60s), WriteTimeout (60s), IdleTimeout (120s), and an `http.MaxBytesReader`-enforced body cap (256 MB for `/sync`, 16 KB for `/pair`).
- Path traversal and inode exhaustion in unpacked LocalStorage / IndexedDB tarballs. v0.12 rejects payloads over 256 MB, tarballs with more than 100,000 entries, members whose path resolves outside the staging directory, and symlink / hardlink / device members.
- Listener bound to non-tailnet interfaces. v0.12 refuses to start the sink or pair listener on `0.0.0.0` and reads the Tailscale 100.x interface directly (or explicit loopback only when configured for local dev).
- Third-party data plane leakage. v0.12 has no hosted relay. Cookies never leave the user's tailnet.

## What agentcookie does not protect against

- Root or sudo on either machine. Anyone with privileged access can read raw cookies out of Chrome's SQLite + Keychain. agentcookie does not raise that bar.
- Compromise of Chrome itself. A malicious extension on the source or sink, a NaCl exploit in Chrome, or a malicious .dylib injected into Chrome can already read cookie plaintext. agentcookie does not change that.
- Compromise of the user's macOS account where the attacker can convince macOS that they ARE the agentcookie binary. The `-T` ACL pins the binary's Developer-ID-signed designated requirement, but a sufficiently sophisticated attacker with code-execution-as-user can re-sign their own binary with the same identity if they also stole the developer's signing identity. Out of scope; the bar is "stolen Developer ID Application certificate plus access to a private signing key".
- Tailscale account takeover. Pairing-derived keys live below the Tailscale identity layer, so an attacker on the tailnet still cannot read or sign sync payloads. But they could exhaust ports, run their own sink, or hold the tailnet open for traffic analysis. Out of scope.
- Device-fingerprint-bound sessions. Sites that bind a session to canvas fingerprint, accept-language, screen size, etc. will fail after replication. agentcookie does not (and likely cannot) sync fingerprint hints. Document affected sites in your allowlist comments and re-auth them in-browser on the sink.
- Coercion of the user. If someone makes the user run `agentcookie pair --as sink` against a hostile source, the cookies will flow as designed.
- Cookie value tampering by the source. The source is authoritative; if the source machine pushes cookies for an allowlisted domain, the sink writes them. There is no separate authorization layer per domain.

## Cryptographic specifics

- Cookie at rest in Chrome's own SQLite on each machine: Chrome's existing scheme (AES-128-CBC with per-machine Safe Storage key, PBKDF2-SHA1, salt `saltysalt`, 1003 iters, IV = 16 spaces, v10 prefix). agentcookie reads with the local key and re-encrypts with the destination's local key.
- Cookie at rest in the sidecar SQLite and adapter session files (v0.12 onward): AES-256-GCM under the 32-byte `agentcookie-master` Keychain key, on-disk shape `agc1:` + base64(12-byte nonce || ciphertext || 16-byte GCM tag).
- Pairing key derivation: X25519 ECDH then HKDF-SHA256, salt = pairing code (12 base32 chars uppercase as of v0.12; was 8 in v0.11), info = `agentcookie-pair-v1`, output = 32 bytes.
- Transport AEAD: AES-256-GCM. Pairing-derived 32-byte keys are used directly as the AES-256 key (v0.12: no redundant SHA-256 step). Legacy `security.shared_secret` values still pass through SHA-256 to produce a 32-byte key and must be at least 32 bytes themselves.
- Sequence and protocol version: int64 monotonic Sequence per source (v0.12: nanosecond granularity, persistent across sink restarts), int ProtocolVersion = 1 in every envelope. Bumping the version is a breaking change.

## What changes when

- v0.12 (this release) closes every Critical and High finding from the v0.11 threat survey except U12 (PP CLI sidecar-reader migration). Until U12 lands, the PP CLI consumer side reads v0.11 plaintext sidecars; the sink writer detects the v0.11 client and emits plaintext rather than sealed envelopes.
- v0.13 (planned) will migrate the paired key keystore at `~/.config/agentcookie/keys/<peer>.json` into the macOS Keychain, closing the last on-disk plaintext credential.
- v0.14 or later may add Linux sink support, a Chrome extension on the sink, and one-to-many fan-out. Each of those reopens parts of this document; re-read before adopting.

## Reporting issues

Open an issue at https://github.com/mvanhorn/agentcookie. For sensitive findings, contact the maintainer directly.
