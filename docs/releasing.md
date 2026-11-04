# Releasing agentcookie

A release ships two Linux tarballs (built by GoReleaser) and one signed,
notarized Universal 2 darwin tarball. The order matters: the darwin binary
embeds the SHA256 of each published Linux tarball, because the laptop's
teammate pairing notice pins them in its install line
(`teammate-install.sh <tag> amd64=<sha256> arm64=<sha256>`). The laptop never
downloads a release to compute those digests, so they must be known when
the darwin binary is built.

So the Linux tarballs are built first, hashed, and the digests passed to
the darwin build as ldflags:

```
-X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxAMD64SHA256=<sha>
-X github.com/mvanhorn/agentcookie/internal/cli.releaseLinuxARM64SHA256=<sha>
```

`make release-pinned TAG=<tag>` does this for you. A darwin binary built
without them (plain `make build` or `make release`) is a dev build for
teammates: it sends no pairing notice, and `teammates list`, `status`, and
`doctor` say "this agentcookie build has no release pin; install a release
build". The same happens when the binary's version is not a release tag
(`git describe` output between tags, or a `-dirty` tree).

## Steps (example: v1.3.0-rc.1)

Run on the Mac that holds the Developer ID cert and the
`agentcookie-notary` notarytool profile
(see `docs/runbook-v0.12-codesign.md`).

1. Tag the release commit on a clean tree:

   ```
   git switch main && git pull --ff-only
   git tag -a v1.3.0-rc.1 -m "agentcookie v1.3.0-rc.1"
   ```

2. Build the Linux tarballs into `dist/`. This must come first, and
   nothing may rebuild them after step 3:

   ```
   goreleaser release --clean --config .goreleaser.linux.yaml --skip=publish
   ```

   On a Mac, use the same zig cross-compile setup as before (CGO is on,
   so each Linux target needs a Linux C toolchain). However they are
   built, the result must be
   `dist/agentcookie_1.3.0-rc.1_linux_amd64.tar.gz` and
   `dist/agentcookie_1.3.0-rc.1_linux_arm64.tar.gz` (plus GoReleaser's
   `dist/checksums.txt`). If GoReleaser's `go mod tidy` hook changed
   `go.mod` or `go.sum`, stop: the tree is dirty and step 3 refuses it.

3. Hash the Linux tarballs and build, sign, and notarize the darwin
   binary with the digests embedded:

   ```
   scripts/linux-release-pins.sh v1.3.0-rc.1      # optional preview of the pins
   make release-pinned TAG=v1.3.0-rc.1
   ```

   `release-pinned` checks that `git describe` is exactly the tag, runs
   `scripts/linux-release-pins.sh v1.3.0-rc.1` (which refuses a
   non-release tag, a missing amd64 tarball, or a tarball that disagrees
   with `dist/checksums.txt`), runs
   `make release LINUX_SHA256_AMD64=<sha> LINUX_SHA256_ARM64=<sha>`, and
   then checks `bin/agentcookie` carries both digests. With no arm64
   tarball it pins amd64 only and warns; arm64 teammates are then refused
   by `teammate-install.sh`.

   `make release` first runs `make mac-app`, which builds the
   agentcookie.app launcher (see `internal/macapp`), signs it with the
   same Developer ID identity, notarizes and staples it with the same
   `agentcookie-notary` profile, and zips it to
   `internal/macapp/embedded/agentcookie.zip`. The darwin binary is then
   built with `-tags macapp` and embeds that zip, so a release installs the
   app that makes its LaunchAgents show as "agentcookie" in Login Items.
   Bump `AppRevision` in `internal/macapp/macapp.go` whenever the launcher,
   `Info.plist` or icon changes, so installed copies get replaced.

   The equivalent by hand:

   ```
   make release $(scripts/linux-release-pins.sh v1.3.0-rc.1)
   scripts/linux-release-pins.sh --check-binary bin/agentcookie v1.3.0-rc.1
   ```

4. Package the darwin tarball:

   ```
   scripts/release-tarball.sh v1.3.0-rc.1
   ```

5. Right before publishing, confirm the binary still matches the Linux
   tarballs you are about to upload, then write the release checksums:

   ```
   scripts/linux-release-pins.sh --check-binary bin/agentcookie v1.3.0-rc.1
   (cd dist && shasum -a 256 agentcookie_1.3.0-rc.1_*.tar.gz > checksums.txt)
   ```

6. Push the tag (the install line fetches
   `scripts/teammate-install.sh` at the tag) and publish exactly those
   files:

   ```
   git push origin v1.3.0-rc.1
   gh release create v1.3.0-rc.1 dist/agentcookie_1.3.0-rc.1_*.tar.gz dist/checksums.txt \
     --prerelease --title "agentcookie v1.3.0-rc.1" --generate-notes
   ```

   Drop `--prerelease` for a stable tag.

7. Check the pin on a laptop with `teammates.yaml`: `agentcookie doctor`
   shows `Teammate release pin: v1.3.0-rc.1 pins linux amd64 and arm64`.

If a Linux tarball has to be rebuilt after step 3, redo steps 3 to 5: the
old darwin binary pins digests that no longer match.

## CI

`.github/workflows/release.yml` (enabled by the `RELEASE_CI_ENABLED`
variable) follows the same order: `build-linux` uploads the Linux
tarballs, `build-darwin` waits for it, downloads them into `dist/`, and
runs `make release-pinned TAG=<tag>`, and `publish` uploads those same
Linux tarballs with the darwin tarball.
