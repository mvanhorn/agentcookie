# agentcookie

Peer-to-peer Chrome session replication for AI agents.

Your laptop is logged in to everything. Your AI agents run on a different machine (Mac mini, cloud VM, whatever) and aren't. That gap is `agentcookie`.

## Why

Existing that skill tools are built for humans switching accounts between two laptops they both touch. They assume someone will click "Merge" or open Chrome periodically. agentcookie is built for the opposite workflow: continuous, one-way, unattended replication from the machine you live in to the machine your AI agents act from. No browser required on the sink. No third-party data plane. Pairing-derived keys, allowlists on both sides.

## Status

Pre-release. v0.1 in active development. Watch the repo for the launch announcement.

### What works today

- Unified `agentcookie` CLI: `source`, `sink`, `pair`, `status`, `version`. All support `--json` for agent callers.
- Pairing handshake: X25519 + HKDF-SHA256 salted with a one-time code. Derived 32-byte keys land at `~/.config/agentcookie/keys/<peer>.json` mode 0600.
- Cookie acquisition on macOS: read Chrome cookies SQLite read-only with `immutable=1`, decrypt v10 ciphertext using the Keychain Safe Storage key.
- Cookie write (v0.9): plain v10 mode + `meta.version=18` pin so any kooky v0.2.2 caller on the Mac mini sink reads cookies directly without paste-from-laptop ceremony. Schema-aware INSERT ... ON CONFLICT adapts to Chrome's evolving column set (`top_frame_site_key`, `source_type`, `has_cross_site_ancestor`). Sink stays Chrome-quit so the version migration never fires.
- Keychain access (v0.10): one-time wizard step broadens Chrome Safe Storage ACL via `add-generic-password -A` inside a one-shot LaunchAgent, so every kooky-using CLI on the sink (instacart-pp-cli, bird, future PP CLIs) reads silently from SSH or LaunchAgent context. No per-binary Always-Allow clicks.
- Adapter cookie push (v0.11): after each sink write, agentcookie pushes decrypted cookies directly into each registered PP CLI's local session cache (5 built-ins: instacart, airbnb, ebay, pagliacci, table-reservation-goat). PP CLIs then run from local data on every invocation -- they never touch Chrome cookies or Keychain. `agentcookie wizard verify-adapters` prints the most recent run's per-adapter results.
- Live-Chrome injection: sink probes Chrome's DevTools port and uses `Storage.setCookies` for instant in-memory visibility. Falls back to SQLite write if Chrome isn't debuggable.
- Wire protocol v1: versioned `SyncEnvelope` with monotonic Sequence (replay defense), source hostname, cookies. Documented in `docs/protocol.md`.
- Sink-side allowlist enforcement: defense in depth even if the source is compromised.
- AES-256-GCM transport over HTTP, layered on top of the Tailscale tailnet's WireGuard channel.
- 42 unit tests across `internal/chrome`, `internal/transport`, `internal/config`, `internal/keystore`, `internal/pairing`, `internal/cdp`, `internal/protocol`.

### What's coming

- Long-lived watch mode (fsnotify-driven continuous sync). Today `agentcookie source --once` covers the cron / launchd loop.
- macOS Keychain storage for derived keys. Today the keys live in `~/.config/agentcookie/keys/` at mode 0600.
- `agentcookie pair --rotate` for live key rotation.
- Durable replay defense (nonce or timestamp window in the envelope).
- One-to-many fan-out.
- Linux sink support.

## Quickstart

See [docs/quickstart.md](docs/quickstart.md) for the full five-minute install. Short version:

```
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest
# Drop ~/.config/agentcookie/source.yaml + allowlist.yaml on laptop
# Drop ~/.config/agentcookie/sink.yaml + allowlist.yaml on Mac mini
agentcookie pair --as source     # on laptop, prints code
agentcookie pair --as sink ...   # on Mac mini, with the code
agentcookie sink                 # long-lived on Mac mini
agentcookie source --once        # one-shot sync on laptop (cron this)
```

## Documentation

- [Quickstart](docs/quickstart.md): five-minute install on a laptop-and-Mac-mini pair
- [Architecture](docs/architecture.md): module layout, sync lifecycle, pairing lifecycle, security boundaries
- [Protocol v1](docs/protocol.md): wire format spec for future client implementations
- [Threat model](docs/threat-model.md): what agentcookie does and does not protect against
- [FAQ](docs/faq.md): common questions
- [Install skill](skill/SKILL.md): Claude Code / gstack-style skill for an agent to drive the install

## License

Apache 2.0.
