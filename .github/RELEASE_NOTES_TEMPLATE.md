# agentcookie v1.0.0

Continuous Mac to Linux cookie sync over Tailscale. Your agent runtime wakes up logged in.

## Highlights

- **Mac to Linux sync**: Your Mac's Chrome sessions flow to a Linux agent runtime (Grok Bot, cloud VM, homelab server) via live CDP injection over Tailscale
- **Live CDP injection**: Cookies go directly into Chrome's in-memory store via `Storage.setCookies` - no SQLite write, no Keychain, no libsecret
- **Extra Chrome profile discovery**: Mac profiles (Profile 1, Profile 2, etc.) are auto-discovered and decrypted; extra-profile cookies flow to sidecar, adapters, and live CDP alongside Default profile cookies
- **Tailscale-only transport**: AES-256-GCM sealed envelopes over your tailnet's WireGuard channel
- **Security-by-default**: Linux sinks with missing policy ship nothing; explicit `policy: blocklist` required for sync-all

## Install

Download from the assets below and verify against `checksums.txt`:

| Platform | Archive |
|----------|---------|
| macOS (Apple Silicon and Intel) | `agentcookie_1.0.0_darwin_universal.tar.gz` |
| Linux amd64 | `agentcookie_1.0.0_linux_amd64.tar.gz` |
| Linux arm64 | `agentcookie_1.0.0_linux_arm64.tar.gz` |

The macOS archive is a Universal 2 binary (arm64 + x86_64). One download runs natively on Apple Silicon and Intel.

```bash
# On Mac
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_darwin_universal.tar.gz
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf agentcookie_1.0.0_darwin_universal.tar.gz
sudo mv agentcookie /usr/local/bin/

# On Linux
curl -LO https://github.com/mvanhorn/agentcookie/releases/download/v1.0.0/agentcookie_1.0.0_linux_amd64.tar.gz
sha256sum -c checksums.txt --ignore-missing
tar -xzf agentcookie_1.0.0_linux_amd64.tar.gz
sudo mv agentcookie /usr/local/bin/
```

Or build from source: `go install github.com/mvanhorn/agentcookie/cmd/agentcookie@v1.0.0`

See the [README](https://github.com/mvanhorn/agentcookie/blob/main/README.md) for the full how-to.

## Honest limits

- **Linux SQLite write is 0**: Expected. Success is the `live_cdp: injected N cookies into M context(s)` line.
- **Omitted policy ships nothing**: On Linux, missing `blocklist.yaml` or omitted `policy:` means allowlist-empty. Write `policy: blocklist` with `domains: []` for sync-all on a trusted box.
- **Google/DBSC cookies**: Need local sign-in on the sink. Copied cookies expire in minutes.
- **Linux extra-profile SQLite stays unread**: Discovery and doctor/status name stores, but Chrome SQLite decryption requires macOS Keychain (no libsecret). Sidecar/plaintext and live CDP remain the Linux path.
- **CDP port is loopback-only**: Same-user processes can attach to `127.0.0.1:9223` and read injected cookies. This is the same-user trust boundary.
- **Cookie values never logged**: Cookie values do not appear in logs or doctor output.

## Security

- Tailscale 100.x bind required on Linux sinks; refuses to start on 0.0.0.0
- AES-256-GCM sealed envelopes with pairing-derived 32-byte keys
- Per-IP rate limiting on the pair endpoint (5 attempts before 429)
- Persistent replay defense via sequence tracking

## Support

Run `agentcookie doctor --json` on both machines and include the output when reporting issues.

## What's in this release

{{CHANGELOG_BODY}}
