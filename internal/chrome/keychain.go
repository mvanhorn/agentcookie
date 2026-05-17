// Package chrome reads and writes Chrome cookies on macOS, handling the
// per-machine Safe Storage encryption via the macOS Keychain.
package chrome

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"fmt"
	"os"
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
// Keychain. On darwin+CGO builds, tries the keybase/go-keychain API path
// first (SecItemCopyMatching), then falls back to shelling out to the
// `security` CLI. The keybase path is more reliable from LaunchAgent
// contexts where the legacy `security` CLI sometimes returns empty output
// or non-zero exits despite the underlying keychain being readable.
//
// Either path requires the binary to be in the Keychain item's ACL OR the
// item to be marked any-app-accessible OR running in a context where the
// login keychain is auto-unlocked (LaunchAgents in the user GUI session).
// Plan 2026-05-17-004's wizard set-keychain-access step is what makes that
// last condition true for new binaries.
func SafeStoragePassword() (string, error) {
	if pw, err := safeStoragePasswordViaKeybase(); err == nil {
		return pw, nil
	}
	// Fall back to `security` CLI shell-out.
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

// DefaultPartitionList is the partition string SetSafeStoragePartitionList
// uses when its argument is empty. Apple-signed binaries and Apple-tool
// callers (like the `security` CLI) read Chrome Safe Storage without a GUI
// prompt under this list. Ad-hoc-signed Go binaries (most PP CLIs) still
// need a one-time "Always Allow" click on first read; the partition list
// is groundwork, not a complete grant.
const DefaultPartitionList = "apple-tool:,apple:"

// buildPartitionListArgv returns the argv for the `security` command that
// updates the Chrome Safe Storage partition list. Split out from
// SetSafeStoragePartitionList so the argv shape is unit-testable without
// shelling out.
func buildPartitionListArgv(partitions string) []string {
	if partitions == "" {
		partitions = DefaultPartitionList
	}
	return []string{
		"set-generic-password-partition-list",
		"-S", partitions,
		"-s", keychainService,
		"-a", keychainAccount,
	}
}

// SetSafeStoragePartitionList expands the partition list on the Chrome
// Safe Storage Keychain item so headless callers can read it with no GUI
// prompt. macOS prompts the user for their login keychain password the
// first time this runs; subsequent runs with the same list are no-ops.
// Idempotent.
//
// Passing partitions = "" uses DefaultPartitionList. Ad-hoc-signed
// binaries (most Go CLIs) are NOT covered by the default and still need
// their own one-time Always Allow click on first read; the partition list
// covers Apple-tool intermediaries (e.g., the `security` CLI itself).
func SetSafeStoragePartitionList(partitions string) error {
	argv := buildPartitionListArgv(partitions)
	cmd := exec.Command("security", argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("set Chrome Safe Storage partition list (login keychain password required): %w", err)
	}
	return nil
}
