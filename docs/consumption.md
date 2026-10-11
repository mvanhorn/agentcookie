# Consuming synced cookies and secrets

agentcookie syncs your live browser sessions and per-CLI secrets from a source
machine to a headless sink. This doc covers the other half: how a tool on the
sink actually consumes what was synced, keychain-free.

The core rule: consumers shell out to agentcookie, they never import it. The
agentcookie module is private; published CLIs cannot depend on it (a
`private_dep_guard` test enforces this in cli-printing-press). Shelling out is
the same pattern CLIs already use for the `press-auth` companion.

## Why not just read Chrome's keychain

On macOS, an ad-hoc-signed Go binary (what `go install` produces for every PP
CLI) cannot be durably granted access to Chrome's Safe Storage keychain item.
That is why per-app push adapters exist, and why a CLI's `auth login --chrome`
path hangs on a headless sink with no one to click the Keychain prompt. The
consumption path below avoids the keychain entirely by reading agentcookie's
own plaintext stores.

## Cookies

agentcookie writes every synced cookie to a local plaintext sidecar
(`~/.agentcookie/cookies-plain.db`). Read a domain's cookies with one call:

```
agentcookie cookies --domain .amazon.com
# Cookie header: session-id=...; session-token=...; x-main=...

agentcookie cookies --domain .amazon.com --json
# [{"name":"session-token","value":"...","domain":".amazon.com",...}, ...]
```

Behavior:

- Universal. Any tool, any domain, regardless of how the CLI was built. Cookies
  need no per-tool configuration; a Cookie header is a Cookie header.
- Keychain-free on a headless sink. When `sink.yaml` sets
  `skip_chrome_sqlite: true`, it reads only the plaintext sidecar and never
  touches Chrome Safe Storage. Otherwise, on macOS it also decrypts cookies
  from Chrome profiles discovered on this machine, which reads each browser's
  Safe Storage key from the Keychain (and can prompt if this binary has no
  grant). The sink daemon follows the same rule, and with `skip_chrome_sqlite`
  off it reads those profiles only when a push adapter's CLI is installed. A
  refused read (no grant or timeout) is not retried until the sink restarts.
- Scoped. Matches the exact host and its subdomains, never look-alikes
  (`.amazon.com` matches `amazon.com` and `www.amazon.com`, never
  `evilamazon.com`).
- Blocklist-enforced. Honors the same opt-out blocklist the sink applies.
- Empty is not an error. A missing sidecar or no match prints nothing and exits
  0, so a consumer can fall through to its own auth path.

A CLI's auth step should try `agentcookie cookies` first (via `exec.LookPath`),
and fall back to its existing path when agentcookie is absent or returns
nothing. agentcookie is always a soft dependency: tools must work without it.

## Secrets

Per-CLI secrets sync to `~/.agentcookie/secrets/<cli>/`. Emit them as
shell-assignable lines:

```
eval "$(agentcookie secret env tesla-pp-cli)"
```

### Key-name mapping

A CLI reads its token from a specific env var (for example tesla-pp-cli reads
`TESLA_AUTH_TOKEN`). The secret may have been imported under a different name
(for example `OAUTH_BEARER` from an `auth.json`). Map the consumer's declared
name to the synced key with an alias, resolved live so it tracks refreshes:

```
agentcookie secret alias tesla-pp-cli TESLA_AUTH_TOKEN OAUTH_BEARER
agentcookie secret env   tesla-pp-cli
# ...
# OAUTH_BEARER=<live value>
# TESLA_AUTH_TOKEN=<same live value>
```

Aliases are explicit. agentcookie never guesses which synced key is the right
one (it will not pick a bearer over a refresh token for you). The mapping is a
deliberate, one-time operator action.

### Finding mismatches

`agentcookie discover` shows a COVERAGE column, and `agentcookie doctor` has a
`Secret coverage` check, that flag any CLI whose synced secret store does not
provide the auth env var it reads, with the exact `secret alias` command to fix
it. A CLI that reads its value in place (no explicit secret store) is shown as
`in-place` and is not flagged.

This per-CLI mapping is what makes secrets printing-press-aware: agentcookie can
only know a consumer wants `TESLA_AUTH_TOKEN` because the CLI declares it. The
authoritative mapping ultimately belongs in the per-CLI manifest the press
emits; the alias above is the operator-set bridge until then.

## Contract summary

| Surface | Command | Keychain | Per-tool config |
| --- | --- | --- | --- |
| Cookies | `agentcookie cookies --domain <d>` | none with `skip_chrome_sqlite`; Safe Storage read on macOS otherwise | none (universal) |
| Secrets | `eval "$(agentcookie secret env <cli>)"` | none | alias when names differ |

Per-app push adapters (`internal/sinkpush`) remain as a legacy fallback; the
read commands above are the supported, generic consumption path.

## Linux consumption (v1.0+)

On Linux, agentcookie runs as a continuous `/sync` sink over Tailscale, just
like macOS. The key difference: it injects cookies directly into a running
Chrome via CDP instead of writing Chrome's SQLite.

### Setup

1. **Join the tailnet**: The Linux sink MUST have a Tailscale 100.x address.
   Verify with `tailscale status`. The sink will refuse to start without it.

2. **Write sink.yaml and blocklist.yaml**: Do NOT run `wizard install --as sink`
   on Linux (it omits the policy file, which means allowlist-empty / ship nothing).
   Write the YAML files directly as shown in the README.

3. **Pair with the source Mac**:
   ```bash
   # On Mac (source):
   agentcookie wizard install --as source --peer <linux-tailscale-hostname>
   # The wizard prints a pairing code and URL

   # On Linux (sink):
   agentcookie pair --as sink --peer <mac-hostname> \
     --pair-url http://<mac-hostname>:9998/pair --code <code>
   ```

4. **Start Chrome with CDP enabled**:
   ```bash
   google-chrome --remote-debugging-port=9223 &
   ```

5. **Start the sink** (binds to your tailnet IP):
   ```bash
   agentcookie sink
   ```

6. **Cookies sync continuously**. Whenever the source Mac's Chrome cookies
   change, they're pushed to the Linux sink and injected via CDP. Any page
   Chrome loads (including agent-driven pages via Playwright, Puppeteer, or
   browser-use) sees the logged-in session.

### Verifying success on Linux

The sidecar (`~/.agentcookie/cookies-plain.db`) and `agentcookie cookies --domain`
DO work on Linux but are NOT the success metric. The sidecar is plaintext at rest.

Success is the live CDP inject. Verify with:

```bash
agentcookie status --json
# Look for LastWriteMode containing "livecdp"

agentcookie doctor
# Look for "live_cdp: endpoint reachable" OK
# Look for "live_cdp: injected N cookies into M context(s)" in sync output
```

The message `wrote 0 cookies` for Chrome SQLite is expected on Linux.

Do NOT use `agentcookie cookies --json` as a verify step - it reads the plaintext
sidecar, which is not the success path.

### Linux cookie policy

Missing `blocklist.yaml` or an omitted `policy` field defaults to **allowlist-
empty** on Linux (ship nothing). This is security-by-default.

For a single-operator trusted box (like your own Grok Bot VM), write:

```yaml
# ~/.config/agentcookie/blocklist.yaml
version: 1
policy: blocklist
domains: []
```

This syncs all cookies. For multi-user or less-trusted sinks, use allowlist mode:

```yaml
version: 1
policy: allowlist
domains:
  - pattern: "github.com"
  - pattern: "%.github.com"
  - pattern: "%.openai.com"
```

### Workflow example: Grok Bot

```bash
# Grok Bot Linux VM at 100.87.49.2 on the tailnet

# 1. Start Chrome with CDP
google-chrome --remote-debugging-port=9223 --headless=new &

# 2. Start the sink (binds to 100.87.49.2:9999)
agentcookie sink

# Grok Bot's browser now syncs continuously with your Mac's sessions
```

### Secrets bus on Linux

Per-CLI secrets sync to `~/.agentcookie/secrets/<cli>/` and work on Linux the
same as macOS. Use `agentcookie secret env <cli>` to emit shell-assignable lines.

### Per-CLI adapters on Linux

Per-CLI push adapters run on Linux when the sidecar is present. However, the
primary consumption path on Linux is the browser via CDP. CLIs that need
specific session files can read the sidecar or secrets bus directly.
