# Durable live Chrome destinations

A sink can select a dedicated, owned browser instead of the legacy sidecar/SQLite lane:

```yaml
destination:
  kind: live_chrome
  name: live_chrome
  required: true
  chrome:
    user_data_dir: ~/.agentcookie/synced-chrome-live_chrome
    port: 9224
    headed: true
    start: on_boot
    relaunch: after_crash
```

The default Chrome profile is refused, including symlink aliases. `attach` uses an existing loopback CDP endpoint; `ppcli` retains the existing sidecar, SQLite and adapter behavior. When the new key is absent, `live_cdp.enabled` selects attach; otherwise the legacy ppcli lane remains selected. A ppcli instance currently runs separately from live destinations.

The owned browser starts without a visible window. `agentcookie destination open live_chrome` asks the running sink to restore every accepted source and only opens a window once all are ready. The launcher does not equate accepting a snapshot with logging in. Owned browser ports must be free at launch. Before using the endpoint, the sink verifies both Chrome's dedicated-profile command-line argument and browser PID. A private ownership receipt also binds the process's browser websocket UUID so a surviving owned browser can be reattached after a sink crash; an unrelated process on the same port is never adopted.

`relaunch: after_crash` restarts a launched child only after a known nonzero process exit. A normal browser close stays closed and new snapshots remain durable. `destination open` explicitly reopens it. An adopted process after a sink restart has no portable child exit status: an unknown later exit is conservatively held rather than guessed to be a crash. `always` opts into restarting after either kind of exit; `never` holds until an explicit open.

## Accepted, desired, applied and ownership

Under `~/.agentcookie/destinations/<destination>/<source>/`, accepted snapshots, merged desired records, context-incarnation ledgers and an append-only intent journal survive restart. Each browser batch first durably appends every record intent using bounded buffering and one file/directory sync. It then verifies every record, atomically checkpoints the verified ledger once per group, and durably completes the verified intents together. Failed readbacks remain pending. An uncertain journal append stops further mutations for that source until reload, rather than appending behind a potentially torn tail. Recovery prepares superseded reads by group when the adapter supports it. These reduce per-record persistence overhead without removing any durability boundary. Files are atomic, fsynced and mode 0600; when the existing master key is present they are sealed. Refusing a master-key read does not silently downgrade state to plaintext.

Each browser write has a durable intent before it begins. Readback verifies canonical cookie values and relevant attributes, not just names. Cookies are batched by browser context; readback takes one jar snapshot per reconciliation and another after a write. Matching already-verified records do not append new intents on each poll. Journal compaction retains the latest evidence for each item/context. A new Chrome process or browser context gets a full restore.

Only owned records may be removed. A complete snapshot's absent records become revocations; held and unsupported scopes are not changed. Incomplete cookie reads can add available cookies but never infer deletions. Incomplete storage scopes retain their prior desired records. Sink policy is applied to both new and previously owned records. A first owner blocks conflicting sources until revocation is applied. Sink-only cookies survive logout. A source-owned cookie rotated in the sink is overwritten on the next reconciliation; turn that account off to avoid this behavior.

Unpartitioned deletion uses an expired write of the exact cookie key, because `Network.deleteCookies` without a partition key may also remove partitioned siblings. Batched revocations use exact-key expiry for the same reason. Every deletion is confirmed absent. Session-cookie finite expiry and partition keys use the shared source/sink cookie shaping code.

A verified owned default profile carries ownership across Chrome process restarts using a private durable profile identity. Each new browser incarnation still gets a fresh ledger and must read back current values before readiness. Attach endpoints, private contexts and replacement profiles never inherit ownership. Completed old writes cannot revive ownership after deletion or remove a later source's record.

A torn or corrupt intent journal is replaced atomically with its readable prefix before any new append. Status retains `repair_needed` because ownership beyond the corruption cannot be proved; later crash-recovery intents remain readable.

If expected ledgers and their intent history are both missing or corrupt, status reports `repair_needed`, including the source and generation. Existing unidentified browser records are never inferred to be owned from their keys. `agentcookie destination repair live_chrome --yes` reapplies desired state and acknowledges the consequence: previously revoked records whose ownership history was lost cannot be identified or removed automatically.

## Wire negotiation and replay

`GET /capabilities` and `GET /status?source=<hostname>` require the paired request HMAC. Capability and status responses carry an HMAC over the fresh request nonce and exact response bytes. Status also signs the requested source identity, so source-state recovery rejects forged marks and substituted queries. Destination control commands bind their encrypted action to the signed request nonce, rejecting ciphertext substitution and replay. A capability response lists only installed delivery surfaces. This cookie-only implementation advertises cookies; storage capabilities are added when the storage adapter is wired.

A v3 sync response reports accepted and applied generations, pending work, failures, conflicts and readiness per destination. A closed or unavailable browser can accept durable desired state and return `degraded`; it does not claim application succeeded. Source `--once` polls pending status up to ten minutes; watch mode monitors in the background and persists results. The sink updates its diagnostic state as pending work completes. Durable `pending_since` does not reset when newer pending generations arrive; `last_accepted_at` and `last_applied_at` distinguish receipt from verified application. Cap exclusions remain visible after restart through `cap_excluded`. Bounded reconciliation prioritizes never-applied groups, then the oldest applied group generation, with stable tie-breaking.

New sources discover capabilities, cache them for one hour, and invalidate the cache after failed delivery. Legacy sinks receive v2 envelopes without snapshot/storage-record fields; Chromium tarball behavior remains available there. Empty v3 snapshots are delivered to revoke owned records. Pull clients negotiate v3 with `X-Agentcookie-Protocol-Version: 3`; legacy pull clients retain the prior 204 empty-cache behavior.

Transport sequence and semantic generation are separate durable counters. Sequences are allocated and sent serially per endpoint, while separate sinks can proceed concurrently. A new source generation after local state loss is reseeded above authenticated sink accepted marks. Replay rejection can reseed transport sequence above the authenticated sink mark and current time.

Replay state remains `map[string]int64` for old binaries. Destination namespaces are `<source>/<destination>`. The implementation also stores `<source>/@floor` and `<source>/@v3`: a fixed migration floor and the latest legacy mark written by v3. This deliberately clarifies the design's first-use rule: seeding every new destination from the *current* legacy mark would reject the second first-use destination when requests arrive out of order. The shadow marker detects a legacy binary advancing the file during rollback. Reserved names and slash components are rejected.

## Verification

Deterministic tests cover sealed wire login/rotation/logout/policy, v2 fallback, authenticated endpoints, generation loss, reversed first-destination arrival, rollback, store failures, crash before/after browser write, incomplete additive cookies, lost ownership history, new contexts and conflicting owners. `TestChromeDestinationLifecycle` is a real-browser gate covering request-visible cookies, isolated contexts, restore-before-window, close/hold/reopen, sink-only preservation and post-write crash recovery. Run it with `AGENTCOOKIE_LIVE_CDP_TEST=1`, or set `AGENTCOOKIE_E2E_REQUIRED=1` to fail rather than skip when Chrome is missing. All fixtures use temporary HOME/profile/state, mock credentials, and non-default ports.


The full cookie pipeline gate is `TestGeckoLiveCookieSourceSinkOwnedChrome` in `internal/cli`, under build tag `agentcookie_cookie_e2e`. It requires the PR3 Gecko fixture packages in the assembled stack. It reads live Firefox/Zen cookies, uses the actual negotiated encrypted sender and sink HTTP handler, and verifies login, token rotation, production `accounts off`/`on`, and logout via HTTP requests from a dedicated owned Chrome. Account exclusion removes the source payload and owned sink cookie without logging the source browser out; re-enabling restores it. A queued pre-logout snapshot is sent afterward with a new transport sequence and must be rejected without reviving the login. It also checks authenticated status, no initial visible page and preservation of an unowned persistent cookie. No storage adapter is needed.

Compile/list the assembled-stack gate without running browsers:

```sh
go test -tags agentcookie_cookie_e2e ./internal/cli -list '^TestGeckoLiveCookieSourceSinkOwnedChrome$' | grep -Fx TestGeckoLiveCookieSourceSinkOwnedChrome
```

Only after the final browser-test barrier, run it with explicit fixture binaries and required gates:

```sh
AGENTCOOKIE_COOKIE_E2E=1 AGENTCOOKIE_GECKO_REQUIRED=1 \
  go test -tags agentcookie_cookie_e2e ./internal/cli \
  -run '^TestGeckoLiveCookieSourceSinkOwnedChrome$' -count=1 -timeout=10m -v
```

`AGENTCOOKIE_GECKO_BINARY`, `AGENTCOOKIE_CHROME_BINARY` and `AGENTCOOKIE_BINARY` must all point to the prepared fixture binaries. Linux can use headless Gecko; the pinned macOS job uses its headed Zen fixture. Both CI jobs explicitly list and run the named tagged gate, rather than assuming `go test ./...` includes tagged tests. Browser execution remains an outstanding release gate until the actual run succeeds.
