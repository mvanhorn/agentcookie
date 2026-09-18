# agentcookie FAQ

## Why not just use a Chrome extension to sync cookies?

Existing extensions (sync-my-cookie, sync-your-cookie, cookie-share) are built for humans switching accounts between two laptops they both touch interactively. They assume someone will click "Merge" or open Chrome periodically. The agentcookie target is the opposite: continuous one-way replication from a laptop you live in to a Mac mini or cloud VM where AI agents act on your behalf, with no human in the loop on the sink side.

You can certainly stack agentcookie with an extension if you want; they don't fight. But on its own, agentcookie covers the agent-operator workflow without requiring a browser to be running on the sink at all.

## Why Tailscale?

Because the alternative is a hosted relay, which is a third-party data plane for highly sensitive material (session cookies), and that bar is too high for a v0.1 personal-use tool. Tailscale gives end-to-end WireGuard between your devices with zero infrastructure on your part. agentcookie layers its own AEAD on top so the wire format would survive a transport swap (raw SSH, Cloudflare Tunnel, S3-as-bus); Tailscale is the v0.1 default because it works for almost everyone in the target audience already.

## Why doesn't agentcookie sync Firefox / Safari / Arc / Brave / Edge?

Firefox and Safari have different cookie stores entirely; supporting them is real work and would split test surface, so v0.1 stays Chrome stable on macOS. Chromium-derived browsers (Arc, Brave, Edge, Vivaldi) share the cookie format with Chrome and are easy follow-ups - file an issue and they likely land in v0.2.

## What about Linux sinks?

**Fully supported via Tailscale `/sync` (v1.0+).** The Linux receive path is the same continuous sync as macOS-to-macOS, but skips Chrome SQLite entirely (no Keychain, no libsecret) and instead injects cookies into a running Chrome via CDP.

The sink receives the AES-GCM-sealed envelope over Tailscale, decrypts with the pairing-derived key, filters by the sink's policy, and injects via CDP's `Storage.setCookies` into every browser context. The target Chrome must be running with `--remote-debugging-port` (default 9223). This is the Grok Bot / agent-runtime path: the Linux box wakes up logged into your sites without a second login.

**Tailscale required**: The Linux sink MUST bind a Tailscale 100.x address. It will refuse to start without a working tailnet connection. Run `tailscale status` to verify connectivity. Plaintext cookie JSON file transfers are NOT the supported Linux path - the tailnet (pairing + AES-GCM envelope + 100.x bind) is the trust boundary.

**Security**: Missing policy on Linux defaults to allowlist-empty (ship nothing). For a single-operator trusted box (like your own Grok Bot VM), write `blocklist.yaml` with `policy: blocklist` and `domains: []` to sync all cookies:

```yaml
version: 1
policy: blocklist
domains: []
```

The 1.0 release does NOT change this default in code. A later release may flip the default, which would be a breaking change.

## Will syncing cookies log me out of sites on the source machine?

No. The source reads cookies with `immutable=1` (Chrome's recommended read-only flag), and `agentcookie source` never writes to the source's Cookies SQLite. The only writes happen on the sink.

## My agent gets logged out on a particular site after syncing - what happened?

Two causes. First, a few sites bind a session to a device fingerprint (canvas, screen size, accept-language, sometimes TLS JA3); replicating the cookie alone is not enough and the site invalidates the session because the fingerprint differs. Second, the site may use Chrome's Device Bound Session Credentials (DBSC), which ties session refresh to a private key in the source Mac's Secure Enclave. In both cases the sink cannot reproduce the missing factor, so the session drops. Workarounds: run `agentcookie accounts off <domain>` and re-auth in the sink's Chrome directly, or use the pair-agent style remote-browser pattern for those sites. See the next entry for DBSC specifics.

## Does Chrome's device-bound cookie protection (DBSC) break agentcookie?

No, not for the sites you use today. The nuance:

- DBSC is opt-in per site. A cookie is device-bound only if the site's backend asks for it. As of May 2026 the one broad adopter is Google's own account/Workspace cookies (generally available on Chrome for Windows first, rolling out on macOS in the next release). Almost every other site, and every Printing Press CLI agentcookie feeds, is unaffected and syncs as before.
- The secrets bus is untouched. DBSC is a cookie protocol; bearer tokens, API keys, and OAuth refresh tokens on the bus replicate normally.
- For a DBSC site, a copied cookie works on the sink only for its short-lived window (minutes) because the sink cannot sign the refresh challenge. agentcookie flags these in `agentcookie doctor` and ships them with a warning by default; pass `--skip-dbsc-suspect` (or set `AGENTCOOKIE_SKIP_DBSC_SUSPECT=1`) to drop them.
- For Google sessions, copying cookies was never the right tool. Sign the sink's Chrome into the same Google account once and it establishes its own device-bound session locally, no copy needed.

See [docs/threat-model.md](threat-model.md) for the full treatment.

## Can I use one source with multiple sinks?

Not in v0.1. One-to-many fan-out is a planned v0.2 feature. The protocol envelope is shaped so the sink doesn't need to know about other sinks, but the source-side state and config are single-peer today.

## What's in the keystore on disk?

`~/.config/agentcookie/keys/<peer>.json` is a small JSON file at mode 0600 containing the 32-byte paired key (base64), the peer hostname, paired_at timestamp, key fingerprint, and protocol version. macOS Keychain storage is a v0.2 hardening item; today the file mode + your OS user separation is the protection.

## Why is the sink's cookie policy independent from the source's?

Defense in depth. The source filters cookies before sending (bandwidth + privacy optimization), but the sink owner ultimately controls what state lands in their Chrome. If the source machine is fully compromised and an attacker tries to push cookies for a domain the sink policy rejects, the sink drops them. Keep both policies in sync if you want the simplest behavior; let them diverge if you want the sink to be more conservative than the source.

## How do blocklist and allowlist mode differ?

`blocklist.yaml` is still the compatibility file. With omitted `policy` or
`policy: blocklist`, agentcookie syncs everything except matching patterns; a
missing or empty file means sync-all. With `policy: allowlist`, only matching
patterns sync and all other cookie hosts are dropped on both source and sink.
Use allowlist mode for high-trust/headless agent deployments where only a small
set of sessions should leave the source machine.

## What about durable replay defense?

In v0.1, the sink rejects an envelope whose Sequence is not strictly greater than the highest seen for that source - but the state is in-memory, so a sink restart clears it and an attacker who captured a payload could replay it once before the next legitimate sync. v0.2 adds a nonce-or-timestamp window to the envelope for durable replay defense; the protocol field for it already has space.

## Is the shared_secret fallback safe?

It's safer than nothing if you use a strong randomly-generated secret and never reuse it. It's strictly worse than pairing because:
- The secret sits in two YAML files unencrypted (file mode 0600 helps, but a compromised user account reads it trivially).
- Rotation requires manually editing both files.
- The MITM defense (pairing code in HKDF salt) is missing.

After pairing once, delete the `security.shared_secret` field from both YAMLs. The fallback exists only for the brief migration window from earlier prototypes.

## Can I run this in a Docker container on a cloud VM?

Yes. The sink can run anywhere Chrome stable runs and the tailnet reaches. On Linux, the sink injects cookies via CDP into a running Chrome - no SQLite write, no Keychain. Start Chrome with `--remote-debugging-port=9223` and configure `live_cdp.endpoint` in sink.yaml. The sink must bind a Tailscale 100.x address; run `tailscale status` to verify connectivity.

On macOS-in-the-cloud (e.g. MacStadium), the full macOS sink path works as long as you can SSH in and the tailnet reaches the host.

## Is this open source?

MIT. PRs welcome. See the repo at https://github.com/mvanhorn/agentcookie.

## How do I report a security issue?

Open an issue at https://github.com/mvanhorn/agentcookie/issues, or for sensitive findings, send a direct message to the maintainer on X at https://x.com/mvanhorn. There's no bug bounty yet; that's not v0.1 territory.
