# agentcookie threat model

This document captures what agentcookie does and does not protect against. Read it before deploying anywhere you care about; the absence of a threat in this list is a statement of scope, not of safety.

## What agentcookie does

Continuously replicates Chrome session cookies for a user-allowlisted set of domains from one macOS machine (the **source**, where the user logs in) to another (the **sink**, where AI agents run). The replication is one-way, opt-in per domain, and authenticated end-to-end with a pairing-derived symmetric key.

## Trust model

agentcookie trusts:

- The OS on both machines, including the kernel, the user account boundary, file mode 0600, and the Chrome process.
- macOS Keychain to store the Chrome Safe Storage password (read with `security find-generic-password`).
- The Tailscale tailnet for transport-layer confidentiality and identity. agentcookie layers its own AES-GCM on top, but the tailnet's WireGuard channel is the first line of defense.
- The user's local filesystem under `~/.config/agentcookie/`. Anyone with read access to that directory can read the paired keys.
- The Chrome stable channel's documented cookie storage and DevTools Protocol behavior.

## What agentcookie protects against

- **Plaintext cookies in transit.** Every payload is AES-256-GCM-sealed with a per-pair key. The key is never written unencrypted anywhere on the wire.
- **Man-in-the-middle during pairing.** X25519 + HKDF salted with the pairing code means an attacker who intercepts the channel without knowing the code derives a different key, and the next AEAD message fails its tag check.
- **Replay of captured payloads (within a process lifetime).** The sink rejects an envelope whose Sequence is not strictly greater than the highest seen for that source. The protection resets on sink restart; durable replay defense is v0.2.
- **Source pushing non-allowlisted cookies.** The sink reads its own `allowlist.yaml` and drops cookies whose host_key matches no pattern, even if the source tries to push them. The sink owner has the final say on what lands in their Chrome.
- **Wrong-secret / unauthenticated requests.** Both legacy `security.shared_secret` and paired keys gate every `/sync` call; AEAD tag mismatch -> 401.
- **Third-party data plane leakage.** v0.1 has no hosted relay. Cookies never leave the user's tailnet.

## What agentcookie does not protect against

- **Root or sudo on either machine.** Anyone with privileged access can read raw cookies out of Chrome's SQLite + Keychain. agentcookie does not raise that bar.
- **Compromise of Chrome itself.** A malicious extension on the source or sink, a NaCl exploit in Chrome, or a malicious .dylib injected into Chrome can already read cookie plaintext. agentcookie does not change that.
- **Compromise of the user's macOS account.** Any process running as the user can read `~/.config/agentcookie/keys/*.json` despite mode 0600. macOS Keychain integration in v0.2 will tighten this against non-Chrome processes that happen to run as the user.
- **Tailscale account takeover.** Pairing-derived keys live below the Tailscale identity layer, so an attacker on the tailnet still cannot read or sign sync payloads. But they could exhaust ports, run their own sink, or hold the tailnet open for traffic analysis. Out of scope.
- **Device-fingerprint-bound sessions.** Sites that bind a session to canvas fingerprint, accept-language, screen size, etc. will fail after replication. agentcookie does not (and likely cannot) sync fingerprint hints. Document affected sites in your allowlist comments and re-auth them in-browser on the sink.
- **Replay of payloads across sink restarts (in v0.1).** Sequence state is process-local. A captured payload could be replayed once if it arrives between a restart and the next legitimate sync.
- **Coercion of the user.** If someone makes the user run `agentcookie pair --as sink` against a hostile source, the cookies will flow as designed.
- **Cookie value tampering by the source.** The source is authoritative; if the source machine pushes cookies for an allowlisted domain, the sink writes them. There is no separate authorization layer per domain.

## Cryptographic specifics

- **Cookie at rest on each machine**: Chrome's existing scheme (AES-128-CBC with per-machine Safe Storage key, PBKDF2-SHA1, salt `saltysalt`, 1003 iters, IV = 16 spaces, v10 prefix). agentcookie does not change this; it reads with the local key and re-encrypts with the destination's local key.
- **Pairing key derivation**: X25519 ECDH -> HKDF-SHA256, salt = pairing code (8 base32 chars uppercase), info = `agentcookie-pair-v1`, output = 32 bytes.
- **Transport AEAD**: AES-256-GCM with key = SHA-256(secret). Random 12-byte nonce prepended to ciphertext. Bothpairing-derived keys and legacy `security.shared_secret` go through the same SHA-256 step so the AES key is always 32 bytes regardless of source.
- **Sequence and protocol version**: int64 monotonic Sequence per source, int ProtocolVersion = 1 in every envelope. Bumping the version is a breaking change.

## What changes when

- **v0.2** is expected to land durable replay defense (nonce + timestamp window in the envelope), macOS Keychain storage for paired keys, and `agentcookie pair --rotate` for live key rotation.
- **v0.3 or later** may add Linux sink support, a Chrome extension on the sink, and one-to-many fan-out. Each of those reopens parts of this document; re-read before adopting.

## Reporting issues

Open an issue at https://github.com/mvanhorn/agentcookie. For sensitive findings, contact the maintainer directly.
