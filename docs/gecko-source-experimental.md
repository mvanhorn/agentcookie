# Experimental Gecko sources

Zen, Firefox, LibreWolf and Floorp have separate Gecko source adapters. This
stage provides live cookies, container identities/events, a durable storage
origin inventory, and closed-browser cookie reads. Storage transfer and durable
live-Chrome reconciliation are delivered by subsequent changes in this series.
Do not treat this stage as the full cross-engine authentication release.

Set `browser.name`, an explicit `browser.profile` (name or absolute path), and
`browser.reader: auto|live|file`. Empty profiles resolve only through a matching
installation hash. Legacy `Default=1` and `Locked=1` do not select a profile.
`browser.app_path` can pin an installation. A saved profile's real directory or
`times.json` creation timestamp changing requires a fresh `--profile` selection
or `--accept-profile-change`.

Live mode uses `--remote-debugging-port=9224` and a temporary companion on port
9225 by default. `agentcookie gecko-launcher install --browser firefox --profile
/path/to/profile` writes an opt-in launcher without starting, quitting or
restarting the browser. Quit the browser yourself before opening the launcher.
No preferences are modified. The BiDi endpoint permits same-user browser control.

The companion has cookies, contextualIdentities, tabs, tabHide, webNavigation,
scripting and `<all_urls>` permissions. Temporary installation has no normal
permission prompt; Firefox may show its one-time hidden-tab notice. Its enforced
CSP permits connections only to the configured loopback WebSocket. Tokens and
all companion files are private to the user. The broker owns its own BiDi
session in a separate session group, and the source daemon reconnects after a
restart. It never terminates somebody else's BiDi session or signals a browser.
Removing or disabling the extension does not cause it to be silently reinstalled.
A new browser session rotates the token and installs a fresh companion.

File mode takes Mozilla's `.parentlock` fcntl lock before touching a database,
also checks Linux's legacy lock symlink, copies the cookie database and journals
to a private temporary directory, and recovers only that copy. It reads only the
clean-quit `sessionstore.jsonlz4`, never recovery/previous session files. It is
always incomplete and cannot authorize deletion. A running browser without a
working live channel is an error, not permission to copy its databases.

Sites with state in several containers are held. `browser.containers` is an
ordered preference list (names or numeric IDs; `default` selects ID 0 and `all`
selects ascending IDs). `browser.container_sites: {Work: [example.com]}` overrides
preferences for a site. Selection applies to a whole registrable site, never to
individual cookies. Unknown isolation attributes, private scopes and unrepresentable
partitions fail closed. Both partition ancestor-bit values are preserved.

State is under the selected configuration directory's private `state/` directory
for companion, broker, catalog and pinned profile identity. `status --json`
includes reader mode, reason, profile, catalog and snapshot completeness.

## Verification status

Deterministic tests cover CityHash v1 reference vectors, profile resolution and
identity changes (including cached daemon readers), routing, cookie provenance,
bounded mozlz4, private WAL and genuine hot rollback-journal recovery,
cross-process fcntl refusal during rollback cache spill and WAL reset,
catalog listing/removal, WebSocket authentication,
frame caps, Ping/Pong, Close validation, protocol upgrades with surviving broker
credentials, and BiDi session ownership. Tests use only temporary profiles.

Real Firefox/Zen lifecycle, daemon crash/reconnect, live cookies/partitions,
companion removal/recovery, delayed browser startup, broker loss without retry
storms, actual-browser install hashes, and no-live-database file tracing remain
release gates. Lifecycle scenarios have concrete opt-in fixtures, but have not
been executed here. The actual-browser installation hash check is unresolved;
reference vectors are not a substitute. A killed SQLite writer verifies real hot
rollback recovery deterministically. The implementation VM rejects AF_UNIX sockets
with EPERM, preventing the real Unix broker and browser lifecycle gates there.
No TCP fallback is used to bypass that boundary. Linux Firefox and pinned headed
Zen gates must run in a runner that supports the intended browser/broker processes.

### SQLite writer fault-window instrumentation

`TestLockedSQLiteWriterRefusedBeforeDatabaseOpen` is a Linux-only deterministic
instrumented-writer equivalent of the review's forwarding-VFS pause. It does not
register a custom SQLite VFS. A subprocess holds the real `.parentlock` fcntl lock
while real SQLite writes only the first of two tables and forces cache spill. The
fixture verifies that original main-file bytes changed and the rollback journal
has a nonzero hot-journal header before pausing at a process barrier. The WAL case
performs a real `wal_checkpoint(TRUNCATE)`, verifies zero length, commits a new WAL
generation with different salt, and pauses with another uncommitted spilled write.

For each paused writer, ten `ReadFile` attempts must return `ErrBrowserRunning`.
Linux inotify `IN_OPEN` observes only `.parentlock`, with no database or sidecar
opens. A deliberate database open after the assertions is a positive control for
the instrumentation. Thus refusal is demonstrated inside both unsafe-copy windows,
without depending on a conveniently scheduled concurrent writer or a mocked lock.
This proves the lock-before-open boundary; it does not reproduce the original
review's page-2/page-3 xWrite callback or claim that an unlocked copy is coherent.

The separate private-recovery tests verify a row is WAL-only using an immutable
main-file connection, recover that row and a genuine killed-writer hot journal in
private copies, and compare original DB/WAL/SHM/journal bytes and modification
timestamps. All profiles and writers are disposable; no browser is launched.

### Fixture isolation and native trust limits

The opt-in browser fixtures create `user.js` before the first process starts,
only inside their new private throwaway profile. It disables migration/import,
password saving/autofill and OS authentication, OS client certificates and
enterprise-root import, default-browser prompts, first-run pages, telemetry,
updates, account onboarding, speculative network activity and related background
services. Fixture processes also set Firefox’s test-only
`MOZ_DISABLE_NONLOCAL_CONNECTIONS=1` and disable crash reporting. A fork may
ignore these environment controls. Seeded page navigation uses loopback HTTP; synthetic HTTPS cookie URLs
are metadata only and do not navigate. Normal launchers never write these prefs.

These settings are not an OS sandbox. Firefox forks may ignore unsupported keys,
and native browser/library startup or platform TLS trust evaluation may still
consult OS trust facilities independently of the disabled import preferences.
In particular, this has not proven zero macOS Keychain API/filesystem reads.
Do not run the final macOS gate if the required no-real-Keychain boundary cannot
be verified by the final-test operator. No default-profile creation or migration
flow is permitted as a substitute for the explicit throwaway `-profile` launch.
