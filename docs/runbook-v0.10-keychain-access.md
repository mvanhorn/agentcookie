# Runbook: v0.10 Chrome Safe Storage keychain access on Mac mini

Goal of v0.10: any kooky-using CLI on the sink (instacart-pp-cli, bird,
future PP CLIs) reads Chrome cookies headlessly from SSH or LaunchAgent
context, with no per-binary Always-Allow click and no manual paste.

This runbook covers what the wizard does automatically, what it cannot
do on macOS, and the one-time GUI fallback for when programmatic
mechanism does not succeed.

## What `agentcookie wizard install --as sink` does for keychain access

After the standard pair / config / LaunchAgent steps, the sink install
auto-runs `set-keychain-access`, which spawns a one-shot LaunchAgent in
the user's GUI session and iterates strategies, validating each via the
same Apple Security framework call path kooky-CGO uses:

1. **delete-and-recreate-with-A** (primary; works on fresh installs).
   Best-effort delete of the existing Chrome Safe Storage item, then
   `security add-generic-password -A` with a random password. The `-A`
   flag means "any application may access this item without warning."
   When the item is freshly created, this does not require the login
   keychain password.

2. **partition-list:apple-tool,apple** (fallback). Tries to broaden the
   item's partition list. On macOS 15+ this typically requires the
   login keychain password and will fail from a LaunchAgent context.

3. **trust-list:&lt;binary&gt;** (fallback per `--extra-binary`).
   Updates the existing item with `-T &lt;binary path&gt;` to add the
   specific binary to the per-binary trust list. Requires reading the
   existing password first, which works from LaunchAgent context when
   the keychain is unlocked.

The wizard reports which strategy won:

```
agentcookie wizard: keychain access: delete-and-recreate-with-A
```

If all strategies fail, the install does not abort. It prints a loud
warning and points at this runbook.

## When the wizard succeeds

After the wizard prints `keychain access: <strategy>`, every kooky-using
CLI on the sink reads Chrome cookies silently. Validate:

```
ssh matts-mac-mini '/Users/mvanhorn/go/bin/instacart-pp-cli auth login'
```

Expected: `imported N cookies from Chrome`, exit 0, no GUI prompt fires
on the Mac mini desktop during the run.

Then:

```
ssh matts-mac-mini '/Users/mvanhorn/go/bin/instacart-pp-cli doctor'
```

Expected:

```
  [ok ] session: N cookies from chrome
  [ok ] api: logged in as <Matt>
```

Note `from chrome`, not `from paste`. That confirms the cookies came
through kooky reading Chrome's Cookies file, not from a stale
`auth paste` session cache.

## When the wizard fails (one-time GUI fallback)

If the wizard prints `keychain access: FAILED` (typically because the
Chrome Safe Storage item already exists with a restrictive ACL from a
prior install or from Chrome itself running), do this one GUI step at
the Mac mini desktop (Screen Sharing works):

1. Open **Keychain Access.app**.
2. In the search bar, type `Chrome Safe Storage`.
3. Double-click the row. The info window opens.
4. Click the **Access Control** tab.
5. Select the **Allow all applications to access this item** radio
   button (the top option).
6. Click **Save Changes**.
7. macOS prompts for your login keychain password. Type it once.

The change is durable. After this:

```
ssh matts-mac-mini '/Users/mvanhorn/bin/agentcookie internal keychain-probe'
```

should print `ok len=N` (where N is the password byte length; the
password itself is never printed).

If `ok len=0`, the keychain item exists but the password is empty.
Re-run the wizard:

```
ssh matts-mac-mini '/Users/mvanhorn/bin/agentcookie wizard set-keychain-access'
```

The wizard's `delete-and-recreate-with-A` strategy can now succeed
because the GUI click cleared the prior ACL restrictions, leaving a
clean slate for the recreate.

## Verification checklist

After install (or after the GUI fallback) the sink should pass:

- [ ] `pgrep -f "agentcookie sink"` returns the sink PID
- [ ] `tail -5 ~/.agentcookie/logs/sink.err.log` shows recent
      `probe ok: N cookies round-tripped, meta.version=18`
- [ ] `agentcookie internal keychain-probe` over SSH prints
      `ok len=N` (N &gt; 0)
- [ ] `instacart-pp-cli auth login` over SSH prints
      `imported N cookies from Chrome` and exits 0
- [ ] `instacart-pp-cli doctor` over SSH prints
      `[ok] session: N cookies from chrome` (not "from paste")
- [ ] `instacart-pp-cli doctor` over SSH prints
      `[ok] api: logged in as`

## What we cannot bypass

`security set-generic-password-partition-list` and
`security add-generic-password -U -A` (update an existing item to set
`-A`) both call `SecKeychainItemSetAccessWithPassword` under the hood,
which requires the user's login keychain password. macOS protects this
operation deliberately because broadening keychain access is a
security-sensitive change. There is no headless bypass for modifying
an existing item's ACL. The wizard's `delete-and-recreate-with-A`
strategy side-steps this by deleting first and creating a fresh item;
this works because `SecKeychainAddGenericPassword` on a new item
respects the `-A` flag at creation time without a password prompt
(provided the login keychain is unlocked, which it is inside a
LaunchAgent).

## Security trade-off

After v0.10, Chrome Safe Storage is readable by any process on the
user's account. This is broader than Chrome's default (apps in the
Apple-signed partition only). For an agentcookie sink running headless
on a Mac mini, the practical threat model already assumes:

- The machine itself is a security boundary
- Any process running as the user can already exfiltrate cookies
  via the `~/.agentcookie/cookies-plain.db` sidecar (plaintext,
  mode 0600)
- The bridge's value depends on multiple processes being able to read
  the cookies

If you do not want this trade-off, pass `--skip-keychain-access` to
the wizard. Kooky-using CLIs on this sink will then prompt for
Always-Allow on first run, per binary.
