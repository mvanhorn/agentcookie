# agentcookie

Peer-to-peer Chrome session replication for AI agents.

Your laptop is logged in to everything. Your AI agents run on a different machine (Mac mini, cloud VM, whatever) and aren't. That gap is `agentcookie`.

## Status

Pre-release. v0.1 in active development.

What works today:

- Unified `agentcookie` CLI with subcommands: `source`, `sink`, `pair`, `status`, `version`. All support `--json` output for agent callers.
- Cookie acquisition on macOS: reads Chrome's Cookies SQLite read-only with `immutable=1`, decrypts with the per-machine Chrome Safe Storage key (PBKDF2-SHA1 of the Keychain password, salt `saltysalt`, 1003 iters, IV = 16 spaces, AES-128-CBC, `v10` prefix).
- Cookie write on macOS: schema-aware INSERT ... ON CONFLICT that adapts to Chrome's evolving column set (`top_frame_site_key`, `source_type`, `has_cross_site_ancestor`).
- Pairing handshake: `agentcookie pair --as source` on the laptop prints a one-time 8-char base32 code. Within 10 minutes, `agentcookie pair --as sink --peer <source-host> --pair-url ... --code <code>` on the Mac mini runs an X25519 exchange authenticated by the code. Both sides derive a 32-byte symmetric key via HKDF-SHA256 (info `agentcookie-pair-v1`) and write it to `~/.config/agentcookie/keys/<peer>.json` at mode 0600.
- Transport: AES-GCM over HTTP. After pairing, both sides look up the per-peer key by `peer.hostname` in the YAML config. Legacy `security.shared_secret` still accepted as fallback.
- 30 unit tests across `internal/chrome`, `internal/transport`, `internal/config`, `internal/keystore`, `internal/pairing`.

What does not yet exist:

- Long-lived watch mode (fsnotify-driven continuous sync) -- planned in U6 of the roadmap.
- Live-Chrome injection on the sink via CDP (today the sink writes only when Chrome is closed) -- U4.
- Signed allowlist sync source-to-sink with sink-side enforcement -- U6.
- macOS Keychain storage for derived keys (today the keys are in `~/.config/agentcookie/keys/` at 0600) -- v0.2.
- Threat-model docs, install skill, brand decision, marketing site, public launch -- U7-U11.

## Quickstart

```
# 1. Install (both machines)
go install github.com/mvanhorn/agentcookie/cmd/agentcookie@latest

# 2. Copy example configs
mkdir -p ~/.config/agentcookie
cp examples/source.yaml ~/.config/agentcookie/source.yaml  # on laptop
cp examples/sink.yaml ~/.config/agentcookie/sink.yaml      # on Mac mini
cp examples/allowlist.yaml ~/.config/agentcookie/allowlist.yaml  # on laptop

# 3. Pair (laptop first)
agentcookie pair --as source
# Prints: code XYZW-ABCD; URL http://<laptop-tailnet>:9998/pair

# Then on Mac mini, within 10 minutes:
agentcookie pair --as sink --peer <laptop-tailnet> \
  --pair-url http://<laptop-tailnet>:9998/pair --code XYZW-ABCD

# 4. Run sink (Mac mini, long-lived)
agentcookie sink

# 5. Sync once (laptop)
agentcookie source --once
```

## License

Apache 2.0.
