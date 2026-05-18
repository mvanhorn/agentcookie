# Runbook: v0.12 Developer ID Application codesigning

Goal of v0.12: every agentcookie binary built locally or by CI carries a
stable Developer ID Application signature with Hardened Runtime and a
secure timestamp. That stability is what lets per-binary Keychain ACLs
(U5+) survive every `go install` without re-triggering Always-Allow
prompts.

The signing identity used by this repo by default is:

    Common Name : Developer ID Application: Matthew Charles Van Horn (NM8VT393AR)
    Team ID     : NM8VT393AR

Local builds, `go install`, and CI release builds all sign with this
identity unless `AGENTCOOKIE_SIGN_IDENTITY` is overridden.

## What signing buys us

`codesign -d -r-` against a Developer-ID-signed binary returns a
designated requirement like:

```
identifier "agentcookie" and anchor apple generic
  and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */
  and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */
  and certificate leaf[subject.OU] = NM8VT393AR
```

This requirement does NOT include the cdhash, so it stays byte-for-byte
identical across rebuilds, machines, and `go install` cycles, as long as
the binary name and signing identity are the same. The wizard install
(U5) records that designated requirement in a per-binary Keychain ACL.
Every subsequent rebuild matches the same ACL entry without a prompt.

## How signing happens

- `make build` (or `go build`) produces `bin/agentcookie`. This step
  does NOT require a Developer ID cert; contributors without one can
  still build and test.
- `make sign` (or `make` with no args) signs `bin/agentcookie` via
  `scripts/sign.sh`. Fails fast with a runbook pointer if the identity
  is not on the build machine.
- `make install` runs `go install ./cmd/agentcookie` then signs
  `$(go env GOBIN)/agentcookie`. This is the steady-state developer
  flow.
- CI release builds use GoReleaser. `.goreleaser.yaml` declares a build
  post-hook that invokes `scripts/sign.sh` on every darwin binary
  before archives are zipped. The release workflow
  (`.github/workflows/release.yml`) imports the cert from the
  `CERTIFICATE_OSX_APPLICATION` secret on every tagged release.

## Verify on the build machine

The Developer ID Application cert should already be installed.
Confirm:

```
security find-identity -v -p codesigning
```

Expected output includes one line containing the NM8VT393AR Team ID.

Sign and verify a fresh build:

```
make build
make sign
make verify
```

`make verify` runs `codesign -d -r-` and prints the designated
requirement. The last line should contain `subject.OU = NM8VT393AR`.

Two consecutive clean rebuilds should produce identical designated
requirements:

```
make build && codesign -d -r- bin/agentcookie > /tmp/req1
make clean && make build && codesign -d -r- bin/agentcookie > /tmp/req2
diff /tmp/req1 /tmp/req2   # should be empty
```

## Install the cert on a new build machine

The maintainer's Developer ID Application cert is exportable from the
Mac it was created on:

1. Open Keychain Access on the original machine.
2. Find `Developer ID Application: Matthew Charles Van Horn
   (NM8VT393AR)` in the `login` keychain.
3. Right-click the cert (with the disclosure-triangle expanded to
   include the private key) and choose `Export 2 items...`.
4. Save as a `.p12` file. Set a strong password.
5. On the new build machine, double-click the `.p12` to import it into
   the login keychain. Enter the password.
6. Verify with `security find-identity -v -p codesigning`. The
   NM8VT393AR identity should appear.

For CI, base64-encode the `.p12` and add it as the GitHub Actions
secret `CERTIFICATE_OSX_APPLICATION`. Add the password as
`CERTIFICATE_OSX_APPLICATION_PASSWORD`.

```
base64 -i agentcookie-codesign.p12 -o agentcookie-codesign.p12.b64
gh secret set CERTIFICATE_OSX_APPLICATION < agentcookie-codesign.p12.b64
gh secret set CERTIFICATE_OSX_APPLICATION_PASSWORD
```

Delete the local `.p12` and `.b64` after upload. Never commit either to
the repo.

## Renew when the cert expires

Apple Developer ID Application certs are valid for five years. Renewal
on the Apple Developer portal produces a new cert with the same Team
ID and the same Common Name shape. Because the designated requirement
asserts only `subject.OU = NM8VT393AR` and Apple's anchor, a renewed
cert produces the SAME designated requirement, so existing
per-binary Keychain ACLs continue to match. No re-trust pass is
required after renewal.

Renewal steps:

1. Sign in to https://developer.apple.com/account/resources/certificates.
2. Generate a fresh Developer ID Application cert. Provide the CSR
   from a new private key created via Keychain Access > Certificate
   Assistant > Request a Certificate from a Certificate Authority.
3. Download the `.cer`, double-click to install in the login keychain
   (binds it to the new private key).
4. Optionally revoke the old cert from the Apple portal once builds
   have switched to the new one.
5. Re-export the new combined cert + key as `.p12` and rotate the
   `CERTIFICATE_OSX_APPLICATION` GitHub secret.

## Override the identity (contributor builds)

A contributor who wants to test the build pipeline with their own
Developer ID cert can do so without editing any code:

```
AGENTCOOKIE_SIGN_IDENTITY="Developer ID Application: Jane Doe (XXXXXXXXXX)" \
  make sign
```

Or to skip signing entirely (binary will not pass U5's Keychain ACL
trust step, but is fine for local hacking):

```
make build
# skip `make sign` -- the binary is unsigned (ad-hoc on macOS)
```

## Test signing manually

```
go build -o /tmp/agentcookie-smoke ./cmd/agentcookie
codesign --force --options runtime --timestamp \
  --sign "Developer ID Application: Matthew Charles Van Horn (NM8VT393AR)" \
  /tmp/agentcookie-smoke
codesign --verify --deep --strict --verbose=2 /tmp/agentcookie-smoke
codesign -d -r- /tmp/agentcookie-smoke
/tmp/agentcookie-smoke --version
```

Last line should print the version without a Gatekeeper rejection.

## Troubleshooting

- `errSecInternalComponent` from codesign: the keychain is locked or
  the LaunchAgent / SSH session does not have access to the login
  keychain. Run `security unlock-keychain login.keychain-db` from the
  GUI user session before signing.
- `no identity found`: `security find-identity -v -p codesigning`
  returned nothing. The cert is missing or in a non-default keychain.
  Re-import the `.p12`.
- `The timestamp service is not available`: transient Apple outage on
  `timestamp.apple.com`. Retry. `--timestamp` is required for future
  notarization, do not drop it.
- `make sign` works locally but CI fails with the identity error: the
  GitHub Actions secret is unset, or the imported cert lacks the
  private key. Confirm the `.p12` exported both the cert and the
  private key (Keychain Access shows the private key under the
  disclosure triangle).
