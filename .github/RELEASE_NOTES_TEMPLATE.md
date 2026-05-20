# agentcookie {{VERSION}}

Closed-beta release. Invitation only.

## Install

Download `agentcookie-{{VERSION}}-darwin-arm64.tar.gz` from the assets below, then:

```
tar -xzf agentcookie-{{VERSION}}-darwin-arm64.tar.gz
cd agentcookie-{{VERSION}}-darwin-arm64
./install-beta.sh --as source     # on your MacBook
# or
./install-beta.sh --as sink       # on your second Mac
```

See `quickstart-beta.md` inside the tarball for the ten-minute walkthrough.

## Verifying the binary

```
codesign --verify --strict --verbose=2 agentcookie
# expected: valid on disk / satisfies its Designated Requirement

codesign -d -r- agentcookie
# expected: identifier "agentcookie" ... certificate leaf[subject.OU] = NM8VT393AR
```

`spctl -a` is the wrong assessment tool for this CLI binary - it
expects an app bundle and reports "rejected: not an app" even when
the binary is correctly signed and notarized. Use the `codesign`
commands above instead. The notarization ticket is verified by
Apple's notary service on first launch.

## What's in this release

{{CHANGELOG_BODY}}

## Known limits (closed beta)

- macOS only on both ends (Linux and Windows sinks are on the roadmap).
- Plaintext sidecar at rest is the default. Sealed sidecar infrastructure is wired up but off until U12 PP CLI migration ships in cli-printing-press.
- No live key rotation. To rotate, re-run `agentcookie wizard install` on both sides.
- eBay sessions are fingerprint-bound at the server side; expect `ebay-pp-cli` to fail authentication regardless of sync state.

## Reporting issues

DM the person who invited you. Include the output of `agentcookie doctor --json`.
