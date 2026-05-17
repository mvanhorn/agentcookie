# Runbook: v0.9 soup-to-nuts on a Mac mini sink

End-to-end verification that an agent (Hermes, or a plain SSH session) on
the Mac mini can act as you on a kooky-using PP CLI with no manual paste
step. This is the v0.9 shipping signal.

## Pre-state

- MBP (source) and Mac mini (sink) both paired and running agentcookie
  v0.9+.
- Mac mini Chrome has been launched at least once so the Chrome Safe
  Storage Keychain item exists.
- Mac mini Chrome is currently QUIT. (`pgrep -x 'Google Chrome'` returns
  nothing.)
- instacart-pp-cli installed on the Mac mini.

## Steps

### 1. Re-run the wizard sink install on Mac mini

This expands the partition list and triggers the Always Allow prompt.
You may be asked for your login keychain password once.

```
agentcookie wizard install --as sink --peer <source-host> \
  --code <pair-code> --pair-url http://<source-host>:9998/pair
```

If pairing already exists, the wizard skips that and just runs the
partition-list step. Look for:

```
agentcookie wizard: partition list granted (Apple-tool callers can now read Safe Storage silently)
```

### 2. Push a fresh sync from MBP

```
agentcookie source --once
```

On Mac mini's sink stderr (or `~/.agentcookie/logs/sink.err`), confirm:

```
agentcookie sink: wrote N cookies (+ N sidecar) + ...
agentcookie sink: probe ok: 3 cookies round-tripped, meta.version=18
```

A `probe FAIL` line here is a stop-the-line: the bridge is unhealthy
and the soup-to-nuts run will not work. See the probe diagnostic for
which check failed (app-bound-leaks > 0 means U1 regressed;
meta.version != 18 means U2 regressed or Chrome rewrote the file).

### 3. SSH to Mac mini and run instacart-pp-cli directly

```
ssh mac-mini 'instacart doctor'
```

Expected output:

```
[ok ] api: logged in as <Matt's display name>
[ok ] cookies: read N cookies from Chrome
...
```

If instacart prompts for a Keychain decision (or errors with "User
interaction is not allowed"), the partition list grant did not cover
this binary. Workaround: SSH into Mac mini interactively, run
`instacart doctor` once at the desktop console, click "Always Allow"
on the prompt. Second run from SSH should succeed silently.

### 4. Confirm zero manual paste in the session

Inspect the SSH session output: no `auth paste`, no `dump-instacart`,
no manual clipboard step. The CLI talked to Chrome Safe Storage and
the Cookies SQLite directly via kooky v0.2.2.

This is the shipping signal for v0.9.

## What "soup to nuts" excludes from this runbook

- Third-party kooky callers (bird CLI from last30days, etc.). Try one
  after the instacart run succeeds; same mechanism applies.
- CLIs that use chromedp / DevTools instead of kooky. Out of scope for
  v0.9; they read from a running Chrome's memory and need a different
  path.
- Hermes-driven flows that wrap the CLI in other automation. The
  bridge is shared; success here means Hermes succeeds.

## Rollback

If the bridge breaks and you need cookies on the Mac mini today:

```
# On MBP (source machine):
~/agentcookie/hack/dump-instacart | ssh mac-mini 'instacart auth paste'
```

This is the manual ceremony that worked before v0.9. The v0.9 plan
exists to eliminate it; this is the rollback if v0.9 itself is broken.

## What to capture if the runbook fails

- The exact `agentcookie sink: probe ...` line from `~/.agentcookie/logs/sink.err`
- `sqlite3 ~/Library/Application\ Support/Google/Chrome/Default/Cookies "SELECT value FROM meta WHERE key='version'"`
- `pgrep -x 'Google Chrome'` on Mac mini (must return empty)
- `security find-generic-password -s "Chrome Safe Storage" -a "Chrome"` on Mac mini (the attributes line shows partition list)
- The error text instacart-pp-cli emitted
