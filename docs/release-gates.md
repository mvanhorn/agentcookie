# Gecko release gates

Status: **experimental; not a full-matrix pass**. Local unit/mock checks and
browser-backed gates are separate results. No release claim can substitute for
recorded evidence from the exact candidate commit.

## Required candidate evidence

1. `go vet ./...`, pinned lint, full deterministic tests on Linux and macOS.
2. `AGENTCOOKIE_E2E_REQUIRED=1 go test ./internal/e2e`: a missing browser fails,
   never silently skips. Live cookie assertions include host-only/domain,
   `__Host-`, HTTP, SameSite, session lifetime and partition context.
3. Linux Firefox → Chrome (headless lifecycle plus a separate Xvfb-headed
   combined storage gate): login, rotation, close/reopen restoration before first
   window, logout, accounts off/on, held-container resolution, daemon restart,
   broker loss, crash/intents recovery, inventory without site visits, replay.
4. Linux only: checksum-pinned previous v2 source and sink binaries in a temporary
   HOME, with CDP fixture source and no host credential providers. Never execute
   the old release on a developer Mac or the default macOS CI job.
5. Pinned headed macOS Zen: the same lifecycle plus codec round-trips, fresh
   IndexedDB v1, V+1 schema repair and fixed-version `VersionError`, conflicts,
   blocked upgrades, multi-store atomicity, quota failures and helper-page
   hostile-service-worker/network-silence assertions.
6. Multi-account promise: two containers for one site reach separate destinations
   without account collapse (PR7 scenario 6b).

All harnesses use throwaway HOME/XDG/TMPDIR, explicit fixture credentials,
nondefault ports and profiles, no discovery/adapters, and cleanup on failure.
File-access tracing must prove no running browser database is opened. Logs show
counts/status/version, never cookie or token values. Production permissions and
real profiles are not a substitute for fixtures.

## Workflow policy

`.github/workflows/e2e-zen.yml` pins the Zen DMG and Chrome for Testing archives
by fixed release URL and SHA-256. The pinned job runs on pull requests; a separate
latest-version job is advisory. Updating a pin is a reviewed change, not a
floating “latest” dependency. The pinned check must be configured as required
in repository branch protection by a maintainer. This repository change does
**not** configure or claim that setting.

Open questions for the maintainer: required check names/branch protection;
GUI-Mac `live_chrome` default; whether request-mode cookie-bearing GETs are the
right default after headed interception evidence; source-authoritative overwrite
versus newest-wins for destination-rotated tokens.

## Current execution limits

The implementation VM denies AF_UNIX/netlink browser startup before DevTools.
That is a blocked browser gate, not a passing or skipped acceptance test. GitHub
CI, macOS GUI checks, and old-release interoperability must run on authorized
runners with those capabilities. Publish the candidate SHA, versions, commands,
exit statuses and test logs before removing the experimental label.
