# Gecko storage records and Chrome application

This component shares one IR v1 codec between the source page's MAIN world and
the destination helper's MAIN world. It supports undefined, special doubles,
BigInt, UTF-16 strings, dates (including invalid dates), regular expressions,
sparse arrays, insertion-ordered object properties, Map, Set, ArrayBuffer,
standard typed views, boxed primitives, aliases and cycles. Unsupported platform
objects, functions, symbols, Blob/File and SharedArrayBuffer fail with a named
reason. An unreadable source surface/database remains incomplete and retains its
previous desired data; it is not an authoritative empty snapshot.

Bounds are depth 64, 100,000 value nodes, 16 MiB per encoded record, 10 MiB per
localStorage value in native UTF-16 units, 64 MiB per database and 128 MiB per
surface. Wire sizes are UTF-8 byte counts, including schema/name overhead, not
JavaScript string lengths. Readers use one readonly transaction per database and
cursor events flushed in groups of at most 500. Key-generator probes use an
aborted readwrite transaction. They do not commit a dummy record or advance the
source generator.

## Read modes and lifecycle

The default `intercept` mode requires the broker to intercept the exact nonce
URL before the companion creates a container-specific tab. The companion proves
origin and title in the MAIN world, hides the tab only after that proof, injects
the shared assets, and removes the tab on completion, failure or timeout. A late
`tabs.create` result is removed even if its request already timed out. Each origin
has a three-second budget; a cycle has thirty seconds and resumes by oldest
attempt, so repeated failures cannot starve later origins. The private source
cache retains previous data across daemon restarts.

When the broker is unavailable, storage pauses with a diagnostic. There is no
silent network fallback. Explicit `request` mode makes a real navigation to the
nonce URL with the user's cookies. It proves the origin but does not expect the
server's 404 page to contain the synthetic nonce title. This mode is not a
zero-network promise. These modes' browser acceptance gates remain mandatory.

Companion integration adds `CompanionAssets()` to the private provisioned
extension and dispatches `readStorage` to `AgentCookieReadStorage`. A running
older temporary companion needs an explicit protocol upgrade/reinstall or the
next browser start to acquire these assets; simply restarting the daemon does
not rewrite a live extension.

## Application, ownership and recovery

`NewAdapter(endpoint, base)` composes the existing destination applier with
storage. `AddRecords` converts origin records into per-key destination items and
per-database completeness scopes. The durable engine journals every item before
the browser operation. IDB operations in one database are batched in a single
readwrite transaction; localStorage writes remain independent so quota failure
reports the already-applied items. Every item is then read again and its
canonical value hash compared. An unchanged key is not proof of an unchanged
value. Recovery uses the same read-back interface.

The helper creates a fresh target in the selected browser context, enables
service-worker bypass and Fetch interception before navigation, fulfills only
the synthetic document, and fails every other request from that target. It
proves the actual context, origin and random title before any operation and
closes its target afterward. It passes data as structured Runtime.callFunctionOn
arguments, never interpolates record data into script source, and never browses
an existing user tab. Other same-origin pages are not quiesced: they receive
localStorage events and may react to those events.

Databases, stores and indexes are additive, never removed. A schema sentinel
creates even a completely empty database/schema and verifies compatible schema.
Revocation of that sentinel relinquishes ownership without deleting schema;
record tombstones delete only source-owned records. Extra destination records
and extra schema are retained.

## Clarifications and limitations

- Database, object-store and index names use tagged Str values, not Go strings.
  This clarifies the design's shorthand `name` fields and preserves lone UTF-16
  surrogates all the way through protocol encoding.
- Object IDs are emitted for dates, regular expressions, boxed values and views
  as well as containers/buffers, preserving aliases across every supported kind.
- An absent version-1 database creates its complete schema in its initial
  upgrade. Existing missing stores/indexes require an additive V+1 upgrade after
  closing inspection connections. Incompatible key paths or index options are
  a schema conflict. Blocked upgrades fail instead of hanging indefinitely.
- **Opening an existing IDB database with a lower explicit version throws
  VersionError.** The design's statement that lower versions open normally was
  incorrect. An additive V+1 upgrade can therefore affect a site's fixed-version
  open call. This is surfaced as `schema: upgraded to V+1` by the JS primitive;
  consumers must retain that diagnostic rather than promising site neutrality.
- Explicit numeric key insertion advances Chrome's generator past its maximum
  record key. A source generator higher than that is not restored; the primitive
  reports `generator_gap`. Source probing is tested separately from restoration.
- The canonical codec is deliberately conservative about unusual custom array
  properties and accessors: unsupported values produce an incomplete source
  database instead of being silently flattened.

## Verification and required gates

Run unit/algorithm fixtures:

```
npm ci --ignore-scripts --prefix internal/storagecodec
npm test --prefix internal/storagecodec
go test -race ./internal/storagecodec ./internal/destination
go vet ./internal/storagecodec
```

The Node tests use `fake-indexeddb` for transaction, schema, ownership, UTF-16 and
generator algorithms. They do **not** certify Firefox, Zen or Chromium behavior.
The Go adapter tests use a fake browser runner against the real durable engine,
including per-value verification, ownership conflicts and partial quota retry.

Browser-capable runners must also run:

```
AGENTCOOKIE_BROWSER_TEST=1 node --test internal/storagecodec/browser_test.cjs
AGENTCOOKIE_BROWSER_TEST=1 go test ./internal/storagecodec -run TestHelperBrowser -count=1
```

Both opt-in gates fail, not skip, if an explicitly requested browser cannot
start. All browser profiles and HOME directories are throwaway fixtures. The
Chromium fixture includes the supported structured-clone types, fresh version-1
schema creation, changed values, rollback, schema conflict, lower-version
VersionError, generator probing and owned-only deletion. The helper gate has an
existing service worker and a hostile same-origin storage-event listener and
asserts that the helper nonce request never reaches the fixture server.

Local Dot VM result (2026-11-10): Chromium exits before DevTools startup because
AF_UNIX socket creation is denied with EPERM in process_singleton_posix.cc.
Consequently no real-browser gate passed locally. The combined **headed Zen**
intercepted-hidden-tab path, container isolation and restart/reinstall acceptance
remain release gates. This implementation does not claim those gates are
verified, nor does a passing mock stand in for them.

### Aggregate and transport budgets

The source schedules bounded origin reads across the whole cycle: each surface
is at most 128 MiB, and the combined storage payload is at most 240 MiB. Origins
that do not fit are explicitly incomplete and queued ahead of complete origins
next cycle; sinks retain previously owned data. Removed or excluded origins are
evicted from the source cache. Cache loading checks file size before allocation
and caps streamed reads at 256 MiB. Complete serialized envelopes must fit within
256 MiB including the 28-byte AEAD overhead; oversized envelopes are rejected,
never sent as an authoritative empty snapshot.

Companion responses negotiate ordered, SHA-256 checked 16 MiB chunks. Frames
remain below 32 MiB and aggregate reserved reassembly/sender memory is bounded
at 272 MiB per peer, including responses awaiting decode and queued sends.
Page execution and tab lifetime remain limited to three seconds; transfer uses
only the remaining 30-second cycle deadline. A synthetic 33 MiB RPC takes about
19 seconds under the VM race test, so transfer is not falsely counted as a
three-second page read. These tests are not browser evidence.

Durable IDB records refer to a shared schema sentinel rather than repeating the
schema per record. Reconciliation prepares the schema before reads or writes;
pure tombstone batches never recreate a removed database. Expanded storage item
state is separately bounded at 512 MiB before payload serialization.

Cookie-only commands and source `--dry-run` never execute storage reads. Cached
reader identity includes the requested surfaces. A normal Gecko storage cycle
reads both localStorage and IndexedDB; `--skip-local-storage` disables only
localStorage. The historical IndexedDB environment opt-in applies only to the
legacy Chromium tarball lane. Consented wizard storage previews explicitly opt
into both surfaces and retain the authenticated-navigation warning for request
mode.

The required combined browser gate is
`TestGeckoStorageSourceSinkChrome` in `internal/e2e`. Set
`AGENTCOOKIE_GECKO_STORAGE_REQUIRED=1`, `AGENTCOOKIE_GECKO_HEADED=1`, and explicit
`AGENTCOOKIE_GECKO_BINARY`, `AGENTCOOKIE_BINARY`, `AGENTCOOKIE_CHROME_BINARY`.
It hard-fails missing binaries or a non-headed source. The fixture covers two
container identities, closed seed tabs, storage-only login/logout, all supported
structured-clone value families, hostile service workers and open tabs, hidden
helper cleanup, source broker/companion execution, and the actual encrypted v3
sink pipeline. This gate is compiled but has not been executed in the restricted
VM; no passing Zen or zero-network claim is made.

Schema sentinels are shared additive metadata, not exclusive ownership of a
whole database. Distinct sources can own distinct records in compatible stores;
conflicting record identities remain exclusive and incompatible schema changes
still fail. Exact per-database status takes precedence over origin inventory
status, so a complete origin can revoke a now-absent database even if an unrelated
origin is incomplete.

Production IDB readback is grouped by exact browser context/incarnation and
origin/database. One readonly transaction verifies all requested keys before a
write, and a fresh transaction verifies them afterward. Any write attempt,
including an aborted transaction, invalidates the value snapshot; reconciliation
end clears it. Individual record hashes and intents remain the ownership unit.
A 500-record fixture verifies exactly two batch reads per reconciliation rather
than opening a hidden target for every record. This is algorithm coverage, not a
browser performance result.

The destination readback request and response have a separate 128 MiB cap: the
request includes schema metadata plus grouped key identities, and destination
values may differ from the 64 MiB source database snapshot. Response accounting
includes fixed per-record error reserves and replacement deltas. Unsupported
local values fail only their own key; they cannot block unrelated owned-record
revocation. Schema compatibility remains independently pending after a safe
record-deletion-only transaction. Recovery batches old record identities but
checks each old schema generation separately.

Browser fixture launchers use private HOME, XDG_CONFIG_HOME and TMPDIR,
`--use-mock-keychain`, `--password-store=basic`, and loopback-only DevTools.
The Node fixture owns a detached Unix process group, sends bounded TERM/KILL,
waits for group exit and process reaping, and retains the profile on cleanup
failure instead of deleting state under surviving processes. Pure mock tests
exercise these flags and cleanup behavior without starting a browser.
If Node has already reported the fixture leader exited/reaped, the launcher
never sends another group signal based only on a stale PID. It waits for natural
group disappearance and otherwise fails closed with the temporary profile kept.
