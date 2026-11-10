# Multiple accounts through isolated destinations

A source can keep Personal and Work sessions for the same site in separate
sink-owned Chrome browsers. Use `sinks[].destination` and `sinks[].containers`
as shown in [source-zen-containers.yaml](../examples/source-zen-containers.yaml),
and configure the same names in the sink's `destinations:` list as shown in
[sink-container-destinations.yaml](../examples/sink-container-destinations.yaml).
Existing scalar `sink.url`, singular `destination`, legacy `live_cdp`, and
unnamed source targets retain their existing behavior.

## Account selection

- Each explicit `containers` list is ordered and contains Gecko container names
  or numeric IDs. `default` means container 0; `all` selects the lowest existing
  container per site. Use a single account selector for strict Personal/Work
  separation. Unknown or ambiguous names fail closed.
- Explicit sink selectors form an account boundary, unlike the browser's
  default ambiguity preferences. A site present only in Work is excluded from
  Personal, including after Personal logs out. Exclusion removes only records
  already owned by that destination. It never substitutes the remaining account.
- Explicit sink selectors take precedence over `browser.container_sites`.
  Targets without `containers` inherit the source browser's normal routing.
- Cookies, localStorage and IndexedDB use the same selected container per site.
  One source capture is read once, including the authorized union of storage
  origins. Each destination gets its own projection of that capture, never the
  multi-container union. There is only one companion connection for a profile.
- All deliveries from one cycle share a semantic generation. Each envelope gets
  a distinct monotonically increasing transport sequence, serialized per sink.
  The sink checks replay under `<source>/<destination>` and preserves legacy
  migration high-water marks. Destination order does not alter account selection.
- Named destinations require protocol v3. The source refuses to fall back to a
  v2 sink's shared cookie jar. The sink rejects unknown names with HTTP 400.

## Default pull scope

A source with only explicit per-container targets does not implicitly publish a
separate default-account pull snapshot. To deliberately include that extra scope,
set the existing `source --watch --pull-listen <address:port>` option. The source
then includes its normal browser route in the storage union and serves that
projection without a destination name, retaining the legacy per-source replay
namespace. An ordinary unnamed/default source target also retains default pull
behavior. Pull-only configurations remain compatible.

## Separate owned browsers and launchers

Destination names must match `[a-z0-9][a-z0-9_-]{0,63}`. They cannot alias replay
bookkeeping keys, traverse state directories or differ only by capitalization.
By default profiles are `~/.agentcookie/synced-chrome-<name>`. Each destination
has a distinct debug port. Explicit ports are reserved before defaults are
assigned. Specify ports explicitly to keep them stable when reordering the list.
Profiles must not overlap one another or a default browser profile, including
symlink ancestors. macOS path comparisons conservatively reject case aliases.
Attach endpoints cannot share a normalized loopback port.

Each destination has its own accepted state, desired records, ownership ledger,
intents and context history under `~/.agentcookie/destinations/<name>/<source>/`.
Source delivery hashes, backoff, errors and readiness are also kept separately.
A failure in Work does not mark Personal failed or block its readiness.

On macOS create two readiness-gated launchers:

```sh
agentcookie chrome-launcher install --destination personal
agentcookie chrome-launcher install --destination work
```

These create `~/Applications/agentcookie Chrome personal.app` and
`~/Applications/agentcookie Chrome work.app`, with distinct bundle identifiers.
The historical `live_chrome` launcher keeps `agentcookie Chrome.app`.
Existing bundles are never overwritten. On every platform use
`agentcookie destination open personal` or `agentcookie destination open work`.
The sink restores that named destination before opening its first window.

## Verification

`TestContainerScenario6bWireIsolationLogoutReassignment` exercises encrypted
sender negotiation, cookies/localStorage/IndexedDB, two destination orders,
logout, reassignment, ownership preservation, durable reload and replay rejection
with synthetic browser adapters. `TestContainerPushReadsOnceAndHashesEachRoute`
checks the actual source fanout loop, shared capture/generation, sequence order
and independent deduplication.

The actual-browser final gate is opt-in and has not been claimed as passing in
the restricted VM:

```sh
AGENTCOOKIE_CONTAINER_E2E=1 \
AGENTCOOKIE_GECKO_BINARY=/absolute/path/to/firefox \
AGENTCOOKIE_CHROME_BINARY=/absolute/path/to/chrome \
AGENTCOOKIE_BINARY=/absolute/path/to/fixture-agentcookie \
AGENTCOOKIE_GECKO_HEADED=1 \
go test ./internal/cli -run '^TestContainerScenario6bBrowsers$' -count=1 -timeout=5m
```

It creates a temporary Gecko profile with two extension-created containers and
two temporary Chrome profiles. It verifies identical cookie, localStorage and
IndexedDB keys with different account values, logout and reassignment through
the real sender, sink pipeline and browser adapters. It uses fixture-only HTTP
origins, non-default ports, injected transport keys and no credential discovery.
All processes and temporary profiles are registered for cleanup.

The required Linux Firefox/Chrome job and pinned headed Zen job explicitly list
and run `TestContainerScenario6bBrowsers` with `AGENTCOOKIE_CONTAINER_E2E=1`.
They verify all three fixture binaries are executable and fail if the test is
missing. The fixture uses the production destination factory, converter and
storage adapter, including the engine's production batch-preparation path;
it does not replace the converter with a test implementation. The advisory
latest-Zen job does not substitute for these required pinned-browser checks.
