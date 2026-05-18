# v0.12 security hardening runbook

This runbook covers the install, verify, and recover paths for the v0.12 security work. Read alongside `docs/threat-model.md` (what v0.12 protects against) and `docs/runbook-v0.12-codesign.md` (how the Developer ID signing pipeline works).

## What changed from v0.11

Steady-state user experience is identical. Wizard install still ends with one Keychain unlock click. SSH-driven agent operations like `ssh mac-mini 'instacart-pp-cli carts'` still return data with zero prompts.

What changed behind the curtain:

- Both `agentcookie sink` and the source pair listener refuse to bind on `0.0.0.0`. The wizard install reads the Tailscale 100.x interface directly; if Tailscale is not running or no 100.x address is present, install fails loud with a remediation pointer.
- HTTP server and client paths now share `internal/cli/httpserver` for timeouts and body-size caps.
- Replay defense persists to `~/.agentcookie/sequence.json` (mode 0600) and reloads on sink boot. Sequence granularity is nanoseconds.
- Pair codes are 12 base32 chars (64 bits). Per-IP rate limit fires at 5 wrong attempts.
- A new `agentcookie-master` macOS Keychain item holds a 32-byte sealing key with a per-binary `-T` ACL. The same per-binary list replaces v0.10's `-A` ACL on Chrome Safe Storage.
- The cookie sidecar SQLite and adapter session files seal their secret-bearing fields under the master key.

## One-time install

On a fresh Mac mini sink, the v0.12 wizard install runs the same `agentcookie wizard install --as sink ...` command as v0.11. Steps:

1. Detect the Tailscale 100.x interface and write it as `listen.addr` in `sink.yaml`.
2. Build a trust list naming the agentcookie sink binary (resolved via `os.Executable` + `filepath.EvalSymlinks`) plus each adapter binary passed via `--extra-binary`.
3. Recreate the Chrome Safe Storage Keychain item with the v0.12 `-T` ACL. Replaces the v0.10 `-A` (any-app) ACL.

A single Keychain unlock prompt fires once. After that, every silent read from the sink binary and registered adapter binaries works headlessly.

## At-rest sealing (opt-in, off by default in v0.12)

The sidecar SQLite and adapter session files have at-rest sealing wired up under the `agentcookie-master` Keychain item, but the wizard install does NOT create that Keychain item by default. The PP CLI consumer side of sealing (U12) has not shipped in cli-printing-press yet; turning sealing on without the matching PP CLI release would break v0.11 PP CLI reads.

To opt in once the matching cli-printing-press release lands:

```
agentcookie wizard set-keychain-access --enable-sealing
```

After that command, the `agentcookie-master` Keychain item exists and the next sync writes sealed envelopes to the sidecar and adapter session files. To revert:

```
security delete-generic-password -s agentcookie-master -a agentcookie
```

The sink falls back to plaintext writes on the next sync.

## Verify

```
# Confirm Developer ID signing identity is installed
security find-identity -v -p codesigning
# Expect one identity matching "Developer ID Application: ... (NM8VT393AR)"

# Confirm master key Keychain item is OFF (v0.12 default)
security find-generic-password -s agentcookie-master -a agentcookie
# Expect: "could not be found in the keychain" (sealing off by default)
# After `wizard set-keychain-access --enable-sealing`: expect a "keychain:" line

# Confirm Chrome Safe Storage carries the v0.12 -T allowlist
security dump-keychain | grep -A 5 "Chrome Safe Storage"
# Expect the per-binary `-T` entries; no `-A` line

# Confirm sink is bound to a tailnet 100.x address
lsof -iTCP -sTCP:LISTEN -P -n | grep agentcookie
# Expect entries on a 100.x address; nothing on 0.0.0.0

# Verify the sealed sidecar contains no plaintext cookies
strings ~/.agentcookie/cookies-plain.db | head
# Expect "agc1:..." base64 strings, not raw cookie values
```

## Recover

| Symptom | Cause | Fix |
|---|---|---|
| Sink fails to start with "listen.addr is required" | `sink.yaml` was generated before v0.12 | Re-run `agentcookie wizard install --as sink` to detect the Tailscale interface and write a concrete listen address. |
| Sink fails to start with "Tailscale not running" | `tailscaled` is stopped | Start Tailscale (`tailscale up`) then re-run the wizard install. |
| `agentcookie status` shows `master key: missing` AND sealing is supposed to be on | The `agentcookie-master` Keychain item was deleted or never created | Run `agentcookie wizard set-keychain-access --enable-sealing`. The master key is created with the trust list reapplied. Existing sealed sidecar / adapter session files become unreadable until the next source sync repopulates them. |
| Sealed sidecar present but a PP CLI returns empty cookies | PP CLI is still on v0.11 and reads `value` directly; sees the `agc1:` prefix as gibberish | Either update the PP CLI to import `pkg/sidecar.ReadSidecar` (U12 work in cli-printing-press) or revert sealing with `security delete-generic-password -s agentcookie-master -a agentcookie`. |
| Pair endpoint returns 429 | Per-IP rate limit hit | Wait 500ms per token (max 5 tokens accumulate). Confirm the source's pair URL is correct; a typo in the URL hostname leads to repeated wrong-code POSTs. |
| Pair endpoint hangs | Pre-v0.12 source talking to a v0.12 sink | The PairTimeout (10 minutes) still applies; the 30-second client-side timeout is the new floor. Update both ends to v0.12. |

## What does not change in v0.12

- The paired key store at `~/.config/agentcookie/keys/<peer>.json` is still on-disk plaintext JSON (mode 0600). The threat model has been honest about this since v0.1; v0.13 will migrate to Keychain.
- agentcookie remains macOS-only on both ends. Linux / Windows support is roadmap.
- The replication direction is still strictly source -> sink. There is no fan-out and no two-way merge.
