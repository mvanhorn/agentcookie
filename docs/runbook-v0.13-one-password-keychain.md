# v0.13 one-password keychain onboarding runbook

This runbook covers how a sink machine grants Chrome Safe Storage access so
that **universal** cookie delivery works — the real Chrome `Default` profile
is written and unmodified third-party cookie tools can read the synced
session. It supersedes the keychain-access portions of
`docs/runbook-v0.10-keychain-access.md` and
`docs/runbook-v0.12-security-hardening.md`.

## The headline change

Earlier versions opened Chrome Safe Storage by deleting and recreating the
keychain item (with `-A` or a per-binary `-T` trust list) from inside a
one-shot LaunchAgent. On modern macOS that triggers a storm of GUI
SecurityAgent "Allow" prompts that a headless sink cannot answer, times out,
and downgrades to degraded delivery.

v0.13 replaces that with a single command that runs **entirely over SSH with
no GUI prompt** and **one** login-password entry:

```
security set-generic-password-partition-list \
  -S "apple-tool:,apple:,teamid:<YOUR_TEAM>" \
  -k "<login-password>" \
  -s "Chrome Safe Storage" -a Chrome
```

`agentcookie wizard install --as sink` runs this for you (resolving
`<YOUR_TEAM>` from the agentcookie binary's own code signature and prompting
once for the password). It performs **no delete and no rewrite** of the Safe
Storage item, so the encryption key value is untouched and existing Chrome
cookies stay decryptable.

## Why one password is unavoidable

macOS requires the login keychain password to modify an existing item's
access (`SecKeychainItemSetAccessWithPassword`). There is no headless
bypass. The `-k` flag supplies that password to `security` non-interactively,
which both authorizes the change and unlocks the login keychain for the
call — which is exactly why it works over SSH where the keychain is otherwise
locked (`-25308 User interaction is not allowed`). Zero password entries is
not achievable; one terminal entry (no GUI dialog) is the floor.

## What the partition covers

`apple-tool:,apple:,teamid:<YOUR_TEAM>` grants read access to:

- **`apple-tool:`** — the `/usr/bin/security` CLI, which is the read path for
  the popular unmodified cookie tools: `yt-dlp`, `pycookiecheat`,
  `browser_cookie3`, `gallery-dl`.
- **`teamid:<YOUR_TEAM>`** — Developer-ID-signed binaries from your signing
  team, read via `SecItemCopyMatching`. This covers agentcookie's own daemon
  (CGO) read path and any tool you sign with the same team.
- **`apple:`** — Apple-signed system binaries.

**Boundary (honest):** a truly arbitrary **unsigned** CGO tool (e.g. a
locally-compiled `kooky`/`rookie` binary that calls `SecItemCopyMatching`
directly and is not signed with your team) is not covered by `teamid:` and
does not go through the `security` CLI. To cover it, either sign it with your
team, add it to a trust list, or use the explicit any-app fallback below.

## Install paths

### Interactive over SSH (the normal path)

```
ssh sink 'agentcookie wizard install --as sink ...'
```

You are prompted once for your macOS login password on the SSH TTY. The sink
lands universal; `agentcookie doctor` reports `Cookie delivery: universal`.

### Fully non-interactive (CI / automation, no PTY)

```
ssh sink 'AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard install --as sink ...'
```

The env var supplies the password with no prompt. (It is passed straight to
`security -k` and never logged or persisted by agentcookie. Note it is
briefly visible in `ps` for the lifetime of the `security` call, which is
unavoidable for `security -k`.)

### No password available

If there is no TTY and `AGENTCOOKIE_LOGIN_PASSWORD` is unset, the install
**does not fail** — it lands a working **degraded** sink (agent-native cookie
serving via the sidecar + adapters still works) and prints the exact command
to upgrade later:

```
agentcookie wizard set-keychain-access
# or non-interactively:
AGENTCOOKIE_LOGIN_PASSWORD=… agentcookie wizard set-keychain-access
```

## Verify

```
agentcookie doctor            # expect: Cookie delivery: universal
security find-generic-password -s "Chrome Safe Storage" -a Chrome -w   # readable (apple-tool path)
```

The sink daemon runs in the GUI session, where the login keychain is
unlocked, so its `teamid:` read succeeds once the partition is set. A direct
`security` read from a fresh bare-SSH session can still report
`User interaction is not allowed` if the login keychain has re-locked — that
is a session-lock artifact, not a partition failure; the daemon (GUI session)
is the authoritative reader.

## Verified live on macOS 15.3.1 (2026-05-31)

End-to-end confirmation on the headless sink (moltbot-mini, Apple M-series,
agentcookie binary Developer-ID-signed `NM8VT393AR`):

- `agentcookie wizard set-keychain-access` reported **"Chrome Safe Storage
  partition set and verified readable (apple-tool:,apple:,teamid:NM8VT393AR);
  universal delivery enabled with no GUI prompt"** — one terminal password,
  zero GUI clicks.
- The sink daemon then read the key in its GUI session and wrote the real
  Default Chrome profile with the full synced cookie set (8875 cookies).
- A direct `security find-generic-password -s "Chrome Safe Storage" -a Chrome
  -w` (after `security unlock-keychain`) returned the key — the `apple-tool:`
  path that yt-dlp / gallery-dl / curl-impersonate use.

This **retires the earlier hypothesis that the partition is unusable on
macOS 15.x**. It is usable; the only thing that ever blocked universal
delivery was the duplicate-item race below, not a macOS limitation. The
`--any-app` Always-Allow click is **not** required on the signed-binary path.

`pycookiecheat` reads via the Python `keyring` library (a direct
`SecItemCopyMatching` from an unsigned homebrew python), not the `security`
CLI, so it is the documented **unsigned-CGO** class that `apple-tool:` /
`teamid:` do not cover. Its `-25308` over SSH is expected, not a bug — sign it
with your team or use the `--any-app` fallback below.

## Duplicate-item race (the real blocker, fixed)

If a sink ran in degraded mode with CDP injection, the injector relaunches
Chrome and Chrome recreates its **own** Chrome Safe Storage keychain item. The
keychain then holds more than one item, the partition is set on one while a
reader hits another, and the install's verification read fails — which is what
left the live sink stuck at `delivery: degraded`.

`agentcookie wizard set-keychain-access` now collapses duplicate items to one
**before** setting the partition (value-preserved; it refuses to delete if it
cannot first read the existing value, since a changed value would destroy all
existing cookies). `agentcookie doctor` flags the race directly:

```
[WARN] Cookie delivery: race: N Chrome Safe Storage keychain items exist ...
       Remediation: converge to one item and re-grant: agentcookie wizard set-keychain-access
```

To converge manually, quiesce first so nothing recreates a duplicate
mid-operation, then re-grant:

```
launchctl bootout gui/$(id -u)/dev.agentcookie.sink   # stop the CDP injector
pkill -x "Google Chrome"                              # stop the racer
agentcookie wizard set-keychain-access                # converge + grant
```

## Recover / narrow

Nothing is deleted, so there is no destructive rollback. To narrow access
back to the security-CLI tools only (drop Dev-ID CGO readers), re-run the
partition set without `teamid:`:

```
security set-generic-password-partition-list \
  -S "apple-tool:,apple:" -k "<login-password>" \
  -s "Chrome Safe Storage" -a Chrome
```

## Fallbacks for the unsigned-CGO long tail

`agentcookie wizard set-keychain-access --any-app` (or `--recreate`) opts
into the legacy delete-and-recreate LaunchAgent chain that opens the item to
**any** application (`-A`). This is the only path that covers arbitrary
unsigned CGO tools, but it is GUI-prompt-prone and security-broad — use it
only on a dedicated sink where any local process reading Chrome cookies is
acceptable. The value-preserving guard still refuses to delete the item if it
cannot first read the existing value.
