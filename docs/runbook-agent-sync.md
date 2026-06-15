# Runbook: agent-sync (Chromium agent browsers)

Make a Chromium agent browser -- **browser-use** or vercel-labs
**agent-browser** -- wake up logged into the same sites as your real Chrome,
the way `cmux-sync` does for cmux. This is the Chromium counterpart to the
cmux loop.

## What it does

`agentcookie agent-sync`:

1. Launches a dedicated Chrome on a loopback debug port (its own
   `--user-data-dir`, so the port is honored without `chrome://inspect` and
   your everyday Chrome is untouched).
2. Reads this Mac's Chrome cookies -- decrypt + cookie policy + DBSC filter, the
   same pipeline `source` and `cmux-sync` use.
3. Injects them as **plaintext, over CDP** into every browser context the
   owned Chrome opens, including the context a connector creates for itself.
4. Re-injects when your Chrome cookies change (fsnotify) and injects each new
   context as it appears.

browser-use / agent-browser connect to the owned Chrome via `--cdp-url` and
are logged in.

## Why live injection (and why the alternatives fail)

This was the hard-won finding. Three transplant strategies were tried and
each fails:

- **Cold on-disk profile seed** -- Chrome 127+ App-Bound Encryption makes
  cookies written into a profile's SQLite undecryptable on a normal cold
  launch; Chrome drops every one on load. (Measured: 0 of 13 GitHub cookies
  survived.)
- **`--cdp-url` with a one-time browser-level cookie write** -- browser-use
  opens its own browser context; a browser-level write never reaches it.
- **Playwright `storage_state` file** -- `addCookies` rejects httpOnly +
  persistent cookies, which are the actual session cookies.

Live injection sidesteps all three: cookies go into the **running** browser's
in-memory store via CDP `Storage.setCookies`, addressed per browser context.
Encryption-at-rest never applies, httpOnly + persistent cookies carry fine,
and the per-context addressing reaches the connector's own context. Verified
end to end: browser-use connected to `agent-sync` reads logged-in on
github.com, including the login-gated `/settings/profile`.

## Use it

```bash
agentcookie agent-sync                          # launch + sync, hold until Ctrl-C
agentcookie agent-sync --headed                 # show the owned browser window
agentcookie agent-sync --domain "%github.com"   # limit to matching hosts
agentcookie agent-sync --port 9400              # debug port (default 9400)
agentcookie agent-sync --verbose                # per-cycle counts
```

It prints the connect commands:

```bash
browser-use --cdp-url http://127.0.0.1:9400 open https://github.com
agent-browser --cdp 9400
```

## Limits

- **DBSC / device-bound cookies do not transfer.** Google/Workspace account
  cookies are the broad adopter; those sites may still read logged-out. They
  are not injected and cannot be faked. Everything else (the large majority)
  works.
- **localStorage/IndexedDB auth is not carried yet.** Cookies first; some
  SPAs keep auth in localStorage, which is a planned follow-up.
- **Same-user trust boundary.** The debug port is loopback-only, but any
  process running as you can connect to it while `agent-sync` runs. Stop it
  (Ctrl-C) when you are done driving agent browsers. See
  `docs/threat-model.md`.
- **Keychain.** Run the installed, signed `agentcookie` so reading Chrome's
  Safe Storage key does not prompt (the grant is per-binary).
