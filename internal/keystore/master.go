// Package keystore manages on-machine secrets for agentcookie.
//
// Two distinct stores:
//
//   - The paired-key store at ~/.config/agentcookie/keys/<peer>.json,
//     mode 0600. One file per peer; the JSON contains the 32-byte
//     HKDF-SHA256 output of the pairing handshake.
//
//   - The macOS Keychain item "agentcookie-master" introduced in v0.12,
//     holding a 32-byte random master key with a per-binary trust list
//     (`-T <agentcookie sink path>`). The master key wraps the sealed
//     sidecar SQLite and adapter session files so a non-agentcookie
//     process running as the same user cannot read those plaintext
//     secrets even on a v0.10/v0.11 box where Chrome Safe Storage
//     would otherwise hand over its key.
//
// This file owns the master key half. See seal.go for the AES-256-GCM
// envelope shape and the higher-level Seal / Unseal helpers.
package keystore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MasterKeychainService is the macOS Keychain "service" attribute the
// agentcookie master key item uses. Constant so tests, install scripts,
// and troubleshooters refer to the same string.
const MasterKeychainService = "agentcookie-master"

// MasterKeychainAccount is the account attribute. The combination
// (service, account) uniquely identifies the item inside the user's
// login keychain.
const MasterKeychainAccount = "agentcookie"

// MasterKeyBytes is the master key length (256 bits, matches AES-256).
const MasterKeyBytes = 32

// ErrMasterKeyMissing is returned by ReadMasterKey when no
// "agentcookie-master" item exists in the login keychain. The wizard
// install creates it; runtime callers should error fast rather than
// auto-create.
var ErrMasterKeyMissing = errors.New("keystore: agentcookie-master Keychain item is missing; run `agentcookie wizard install --as sink`")

// ReadMasterKey returns the 32-byte master key from the login keychain.
// Errors with ErrMasterKeyMissing when no item is present; errors with
// a descriptive message on any other failure (locked keychain, ACL
// rejection, decode error). Callers should treat all errors as fatal.
func ReadMasterKey() ([]byte, error) {
	out, err := runSecurity("find-generic-password",
		"-s", MasterKeychainService,
		"-a", MasterKeychainAccount,
		"-w")
	if err != nil {
		// security exits 44 with "The specified item could not be found
		// in the keychain." when the entry is absent. Surface that as a
		// typed sentinel so callers can route to the install runbook.
		if strings.Contains(err.Error(), "could not be found") {
			return nil, ErrMasterKeyMissing
		}
		return nil, fmt.Errorf("keystore: read master key: %w", err)
	}
	hexstr := strings.TrimSpace(out)
	key, derr := hex.DecodeString(hexstr)
	if derr != nil {
		return nil, fmt.Errorf("keystore: decode master key (corrupt Keychain item; delete and re-run wizard install): %w", derr)
	}
	if len(key) != MasterKeyBytes {
		return nil, fmt.Errorf("keystore: master key wrong length %d (expected %d); delete and re-run wizard install", len(key), MasterKeyBytes)
	}
	return key, nil
}

// MasterKeyExists reports whether the Keychain item is present, without
// reading its value. Used by the wizard's idempotent install path so
// re-running wizard install does not regenerate the master key.
func MasterKeyExists() bool {
	_, err := runSecurity("find-generic-password",
		"-s", MasterKeychainService,
		"-a", MasterKeychainAccount)
	return err == nil
}

// CreateMasterKey generates a fresh 32-byte random master key and
// installs it as a generic-password Keychain item with a `-T` ACL
// listing the agentcookie binary plus each binary in extraBinaries.
//
// Idempotent: if the item already exists, the function deletes it
// first. Callers responsible for ensuring this only fires during the
// wizard install ceremony (when an interactive user is present to
// authorize Keychain modifications).
//
// The ACL is anchored to the binaries' Developer-ID-signed designated
// requirement rather than their cdhash, so rebuilding the agentcookie
// binary with the same identity does NOT invalidate the ACL.
func CreateMasterKey(agentcookieBinary string, extraBinaries []string) error {
	if agentcookieBinary == "" {
		return errors.New("keystore: CreateMasterKey requires agentcookieBinary path")
	}
	abs, err := filepath.Abs(agentcookieBinary)
	if err != nil {
		return fmt.Errorf("keystore: abs %s: %w", agentcookieBinary, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("keystore: agentcookieBinary %s does not exist: %w", abs, err)
	}

	keyBytes := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("keystore: read random: %w", err)
	}
	hexKey := hex.EncodeToString(keyBytes)

	// Delete any prior item; ignore "not found" failure.
	_, _ = runSecurity("delete-generic-password",
		"-s", MasterKeychainService,
		"-a", MasterKeychainAccount)

	args := []string{
		"add-generic-password",
		"-s", MasterKeychainService,
		"-a", MasterKeychainAccount,
		"-w", hexKey,
		"-T", abs,
	}
	for _, b := range extraBinaries {
		ab, _ := filepath.Abs(b)
		if ab == "" {
			continue
		}
		args = append(args, "-T", ab)
	}
	if _, err := runSecurity(args...); err != nil {
		return fmt.Errorf("keystore: install Keychain item: %w", err)
	}
	return nil
}

// runSecurity is a tiny wrapper around /usr/bin/security so master.go
// stays testable (the binary lives at a known path on macOS). Returns
// stdout on success, an error containing stdout+stderr on failure.
func runSecurity(args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/security", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("security %s: %w (%s)", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
