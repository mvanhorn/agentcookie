#!/usr/bin/env bash
# Sets the GitHub secrets agentcookie's release workflow needs to sign
# and notarize the Mac build. Secret values are never printed.
#   CERTIFICATE_OSX_APPLICATION           base64 .p12 of ONLY the Developer ID Application identity
#   CERTIFICATE_OSX_APPLICATION_PASSWORD  random password for that .p12
#   AC_API_KEY_P8 / AC_API_KEY_ID / AC_API_ISSUER_ID
#                                         App Store Connect API key for notarytool
#
# Usage: scripts/setup-release-secrets.sh <AuthKey_XXXX.p8> <issuer-id>
# The key ID is read from the .p8 file name. Apple checks the key before
# anything is saved. Create a key at App Store Connect > Users and Access >
# Integrations > App Store Connect API (role: Developer).
set -euo pipefail
[ $# -eq 2 ] || { sed -n 2,13p "$0"; exit 2; }
P8=$1; ISS=$2
KID=$(basename "$P8" .p8); KID=${KID#AuthKey_}
xcrun notarytool history --key "$P8" --key-id "$KID" --issuer "$ISS" </dev/null >/dev/null 2>&1 \
  || { echo "Apple rejected the API key ($KID). Nothing was changed."; exit 1; }
REPO=mvanhorn/agentcookie
IDENTITY="Developer ID Application: Matthew Charles Van Horn (NM8VT393AR)"
WORK=$(mktemp -d); chmod 700 "$WORK"; trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/export.swift" <<'SWIFT'
import Foundation
import Security
let label = CommandLine.arguments[1], out = CommandLine.arguments[2], pass = CommandLine.arguments[3]
var items: CFTypeRef?
let q: [String: Any] = [kSecClass as String: kSecClassIdentity, kSecMatchLimit as String: kSecMatchLimitAll,
                        kSecReturnRef as String: true]
guard SecItemCopyMatching(q as CFDictionary, &items) == errSecSuccess, let ids = items as? [SecIdentity] else { print("no identities"); exit(1) }
var match: SecIdentity?
for id in ids {
    var cert: SecCertificate?
    SecIdentityCopyCertificate(id, &cert)
    if let c = cert, (SecCertificateCopySubjectSummary(c) as String?) == label { match = id }
}
guard let identity = match else { print("identity not found: \(label)"); exit(1) }
var params = SecItemImportExportKeyParameters()
params.version = UInt32(SEC_KEY_IMPORT_EXPORT_PARAMS_VERSION)
params.passphrase = Unmanaged.passRetained(pass as CFString)
var data: CFData?
let st = SecItemExport(identity, .formatPKCS12, [], &params, &data)
guard st == errSecSuccess, let d = data as Data? else { print("export failed: \(st)"); exit(1) }
try d.write(to: URL(fileURLWithPath: out))
SWIFT

P12_PASS=$(openssl rand -base64 24)
echo "Exporting the Developer ID identity. macOS will ask you to allow it: enter your Mac login password and click Allow."
swift "$WORK/export.swift" "$IDENTITY" "$WORK/cert.p12" "$P12_PASS"

base64 -i "$WORK/cert.p12" | tr -d '\n' | gh secret set CERTIFICATE_OSX_APPLICATION --repo "$REPO"
printf '%s' "$P12_PASS" | gh secret set CERTIFICATE_OSX_APPLICATION_PASSWORD --repo "$REPO"

base64 -i "$P8" | tr -d '\n' | gh secret set AC_API_KEY_P8 --repo "$REPO"
printf '%s' "$KID" | gh secret set AC_API_KEY_ID --repo "$REPO"
printf '%s' "$ISS" | gh secret set AC_API_ISSUER_ID --repo "$REPO"
unset P12_PASS
gh secret list --repo "$REPO"
