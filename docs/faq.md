# agentcookie FAQ

## Why not just use a Chrome extension to sync cookies?

Existing extensions (sync-my-cookie, sync-your-cookie, cookie-share) are built for humans switching accounts between two laptops they both touch interactively. They assume someone will click "Merge" or open Chrome periodically. The agentcookie target is the opposite: continuous one-way replication from a laptop you live in to a Mac mini or cloud VM where AI agents act on your behalf, with no human in the loop on the sink side.

You can certainly stack agentcookie with an extension if you want; they don't fight. But on its own, agentcookie covers the agent-operator workflow without requiring a browser to be running on the sink at all.

## Why Tailscale?

Because the alternative is a hosted relay, which is a third-party data plane for highly sensitive material (session cookies), and that bar is too high for a v0.1 personal-use tool. Tailscale gives end-to-end WireGuard between your devices with zero infrastructure on your part. agentcookie layers its own AEAD on top so the wire format would survive a transport swap (raw SSH, Cloudflare Tunnel, S3-as-bus); Tailscale is the v0.1 default because it works for almost everyone in the target audience already.

## Why doesn't agentcookie sync Firefox / Safari / Arc / Brave / Edge?

Firefox and Safari have different cookie stores entirely; supporting them is real work and would split test surface, so v0.1 stays Chrome stable on macOS. Chromium-derived browsers (Arc, Brave, Edge, Vivaldi) share the cookie format with Chrome and are easy follow-ups - file an issue and they likely land in v0.2.

## What about Linux sinks?

On the roadmap. The two pieces that change: Chrome's Safe Storage on Linux uses libsecret (`secret-tool`) with a different encryption flag (v10 vs v11), and there's no macOS Keychain for storing paired keys. Both are tractable but were out of scope for v0.1.

## Will syncing cookies log me out of sites on the source machine?

No. The source reads cookies with `immutable=1` (Chrome's recommended read-only flag), and `agentcookie source` never writes to the source's Cookies SQLite. The only writes happen on the sink.

## My agent gets logged out on a particular site after syncing - what happened?

A few sites bind a session to a device fingerprint (canvas, screen size, accept-language, sometimes TLS JA3). Replicating the cookie alone is not enough; the site invalidates the session because the fingerprint differs. agentcookie cannot fix this in v0.1. Workarounds: remove the site from your allowlist and re-auth in the sink's Chrome directly, or use the pair-agent style remote-browser pattern for those sites.

## Can I use one source with multiple sinks?

Not in v0.1. One-to-many fan-out is a planned v0.2 feature. The protocol envelope is shaped so the sink doesn't need to know about other sinks, but the source-side state and config are single-peer today.

## What's in the keystore on disk?

`~/.config/agentcookie/keys/<peer>.json` is a small JSON file at mode 0600 containing the 32-byte paired key (base64), the peer hostname, paired_at timestamp, key fingerprint, and protocol version. macOS Keychain storage is a v0.2 hardening item; today the file mode + your OS user separation is the protection.

## Why is the sink's allowlist independent from the source's?

Defense in depth. The source filters cookies before sending (bandwidth + privacy optimization), but the sink owner ultimately controls what state lands in their Chrome. If the source machine is fully compromised and an attacker tries to push cookies for new domains, the sink-side allowlist drops them. Keep both allowlists in sync if you want the simplest behavior; let them diverge if you want the sink to be more conservative than the source.

## What about durable replay defense?

In v0.1, the sink rejects an envelope whose Sequence is not strictly greater than the highest seen for that source - but the state is in-memory, so a sink restart clears it and an attacker who captured a payload could replay it once before the next legitimate sync. v0.2 adds a nonce-or-timestamp window to the envelope for durable replay defense; the protocol field for it already has space.

## Is the shared_secret fallback safe?

It's safer than nothing if you use a strong randomly-generated secret and never reuse it. It's strictly worse than pairing because:
- The secret sits in two YAML files unencrypted (file mode 0600 helps, but a compromised user account reads it trivially).
- Rotation requires manually editing both files.
- The MITM defense (pairing code in HKDF salt) is missing.

After pairing once, delete the `security.shared_secret` field from both YAMLs. The fallback exists only for the brief migration window from earlier prototypes.

## Can I run this in a Docker container on a cloud VM?

The sink can run anywhere Chrome stable runs (when CDP is enabled and Chrome is reachable) OR anywhere you can mount the destination's Chrome Cookies SQLite (when only the SQLite path is used). On Linux that means the v0.2 Linux sink work needs to land first. On macOS-in-the-cloud (e.g. MacStadium), it works today as long as you can SSH in and the tailnet reaches the host.

## Is this open source?

Apache 2.0. PRs welcome. See the repo at https://github.com/mvanhorn/agentcookie.

## How do I report a security issue?

Open an issue, or for sensitive findings, email the maintainer directly. There's no bug bounty yet; that's not v0.1 territory.
