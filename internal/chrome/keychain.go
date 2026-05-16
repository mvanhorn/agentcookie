// Package chrome reads and writes Chrome cookies on macOS, handling the
// per-machine Safe Storage encryption via the macOS Keychain.
package chrome

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"fmt"
	"os/exec"
	"strings"
)

const (
	keychainAccount = "Chrome"
	keychainService = "Chrome Safe Storage"
	pbkdf2Salt      = "saltysalt"
	pbkdf2Iter      = 1003
	aesKeyLen       = 16
)

// SafeStoragePassword returns the Chrome Safe Storage password from the macOS
// Keychain. The first call from a new binary prompts the user for Keychain
// access; subsequent calls succeed silently once the user clicks "Always Allow".
func SafeStoragePassword() (string, error) {
	cmd := exec.Command("security",
		"find-generic-password",
		"-a", keychainAccount,
		"-s", keychainService,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read Chrome Safe Storage from Keychain (did you grant access?): %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// DeriveAESKey turns the Safe Storage password into the AES-128 key Chrome uses
// for cookie value encryption on this machine. Salt and iteration count are
// pinned to Chrome's macOS implementation.
func DeriveAESKey(password string) ([]byte, error) {
	key, err := pbkdf2.Key(sha1.New, password, []byte(pbkdf2Salt), pbkdf2Iter, aesKeyLen)
	if err != nil {
		return nil, fmt.Errorf("pbkdf2: %w", err)
	}
	return key, nil
}
