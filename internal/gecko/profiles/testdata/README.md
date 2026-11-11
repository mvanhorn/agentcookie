# CityHash reference vectors

`cityhash-v1.json` hashes the byte sequence `byte(i*37+11)` for every length
from 0 through 256. This covers each short-input branch, non-ASCII bytes, and
multiple long-input rounds. The path vectors in `hash_test.go` use UTF-16LE,
including a surrogate pair, without a terminating NUL.

Generated on 2026-11-10 by compiling Mozilla's **CityHash Version 1** C++
implementation with `g++ -shared -fPIC -O2`, calling `CityHash64` with Python
`ctypes`, and saving the unsigned integer results. Modern CityHash libraries
have a different algorithm and are not a valid substitute.

Reference files:
- https://github.com/mozilla/gecko-dev/blob/5836a062726f715fda621338a17b51aff30d0a8c/other-licenses/nsis/Contrib/CityHash/cityhash/city.cpp
  SHA-256: `4999174e36007da7ac159881477bede03f639b420bf9f8bb3ae7de1b240c550e`
- https://github.com/mozilla/gecko-dev/blob/5836a062726f715fda621338a17b51aff30d0a8c/other-licenses/nsis/Contrib/CityHash/cityhash/city.h
  SHA-256: `06da4f67286fc172a580239fc8fa9516438a7d7c99eadb0db176ed570f7feffe`

Mozilla's `toolkit/mozapps/update/common/commonupdatedir.cpp`, `GetInstallHash`,
uses the UTF-16 code-unit byte length and uppercase, unpadded hexadecimal output.
Only synthetic strings and profiles are used by these tests. They do not
constitute the browser-created-profile acceptance gate.

## Browser-generated install registration: acceptance gate still blocked

The reference vectors prove algorithm compatibility only. The browser-created
`profiles.ini` / `installs.ini` install-hash acceptance gate has **not passed**.
Existing lifecycle tests launch with an explicit temporary `-profile` and do
not establish that the resolver matches a browser-generated install section.

The supported Firefox startup CLI cannot exercise normal first-run install
registration while every launch retains an explicit `-profile` argument:

- `SelectStartupProfile` handles `-profile`, selects that directory, and returns
  before the `-createprofile`, `-p`, and first-run/default-selection branches.
- A new temporary `HOME` does not change that branch ordering. Combining
  `-profile` with `-CreateProfile` or `-P` therefore does not test first-run
  registration. `-CreateProfile` alone also is not proof of install-default
  selection.
- Writing an install section from this resolver's own hash, or injecting an
  artificial profile-service registration, is not evidence of normal browser
  startup choosing its installation directory.

Immutable source evidence: Mozilla
[`nsToolkitProfileService::SelectStartupProfile`](https://github.com/mozilla/gecko-dev/blob/5836a062726f715fda621338a17b51aff30d0a8c/toolkit/profile/nsToolkitProfileService.cpp#L1444-L1650).
This source audit is not execution evidence for the checksum-pinned CI binary.

No default-profile startup was added, including in CI. Completing this gate
requires either a separately authorized, isolated first-run startup without
`-profile`, or a verified supported mechanism that produces equivalent native
registration while preserving the explicit-profile constraint. Until then,
report this acceptance criterion as blocked; do not label the vector test or
existing lifecycle gate as install-registration proof. macOS bundle-directory
matching likewise remains a distinct unverified browser gate.

Containment caveat for any future fixture: Firefox checks `XRE_PROFILE_PATH`
and `XRE_PROFILE_LOCAL_PATH` before `-profile`. A fixture must remove inherited
restart/profile variables rather than assuming its CLI argument overrides them.
