//go:build darwin && cgo

package chrome

import (
	keychain "github.com/keybase/go-keychain"
)

// safeStoragePasswordViaKeybase is the darwin+CGO fast path for reading
// Chrome Safe Storage. Calls the same Apple Security framework function
// kooky-CGO uses (via the keybase wrapper) instead of shelling out to
// `security`. The motivating observation: from a LaunchAgent context, the
// keybase path returns the password reliably while the `security` CLI
// occasionally returns exit 44 with empty stdout despite the underlying
// Keychain item being readable.
func safeStoragePasswordViaKeybase() (string, error) {
	password, err := keychain.GetGenericPassword(keychainService, keychainAccount, "", "")
	if err != nil {
		return "", err
	}
	return string(password), nil
}
