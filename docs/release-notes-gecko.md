# Release highlight: experimental Gecko session sources

Zen, Firefox, LibreWolf and Floorp can provide a source profile through a
companion and broker-owned BiDi connection, with closed-browser cookies-only
fallback. New live destinations use dedicated Chrome profiles, durable desired
state, record ownership and verified restoration before the first window.

Onboarding asks which browser you log in with, displays profile-resolution
reason and install hash, explains companion permissions, and keeps ambiguous
container accounts held until selected. The owned Chrome app asks the sink to
restore and open, rather than bypassing the readiness check.

This is **experimental** until the candidate's complete Linux and headed macOS
Zen gates pass. See [the release checklist](release-gates.md). Tests blocked by
host permissions or not run are not passes. A maintainer must separately make
the pinned Zen check required in branch protection.

Migration notes:

- Existing configs are preserved. New GUI Mac installs default to an owned live
  Chrome; Linux/non-GUI installs attach to an explicit loopback CDP endpoint.
- Request-mode storage reads perform cookie-bearing requests. Intercept mode's
  zero-network behavior requires the combined browser and hostile-worker gate.
- Engine/device-bound sessions can still need a fresh login. Gecko DBSC status
  is not applicable, not a portability guarantee.
- IndexedDB schema additions may raise the destination version to V+1; sites
  opening a lower fixed version receive VersionError. Names preserve UTF-16.
- Lost generation state needs authenticated reseeding. A stable replay migration
  floor prevents old envelopes from returning after a v2/v3 transition.
- Accepted generations are not necessarily applied. Review ready, pending,
  failed and repair-needed before declaring a login available.
