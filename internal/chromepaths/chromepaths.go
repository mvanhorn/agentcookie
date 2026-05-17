// Package chromepaths centralizes the on-disk Chrome profile paths
// agentcookie reads and writes. macOS-only. Default profile only (v0.7
// scope limit).
package chromepaths

import (
	"os"
	"path/filepath"
)

// MacChromeProfileRoot returns the user's Chrome user-data-dir on macOS:
//
//	~/Library/Application Support/Google/Chrome
func MacChromeProfileRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// DefaultProfileDir returns the path to the Default profile dir, the only
// profile v0.7 syncs.
func DefaultProfileDir() string {
	return filepath.Join(MacChromeProfileRoot(), "Default")
}

// CookiesDB returns the SQLite path for the Default profile's cookies.
func CookiesDB() string {
	return filepath.Join(DefaultProfileDir(), "Cookies")
}

// LocalStorageLevelDB returns the dir holding the Default profile's
// localStorage LevelDB.
func LocalStorageLevelDB() string {
	return filepath.Join(DefaultProfileDir(), "Local Storage", "leveldb")
}

// IndexedDBDir returns the dir holding the Default profile's IndexedDB
// stores (one subdir per origin).
func IndexedDBDir() string {
	return filepath.Join(DefaultProfileDir(), "IndexedDB")
}

// SidecarCookiesDB returns the agentcookie bridge sidecar path. PP CLIs
// read from this file (or honor the AGENTCOOKIE_PLAIN_COOKIES env var
// pointing at it) to get cookies without Keychain access. Plaintext
// values, Chrome-shaped schema. Default location is
// ~/.agentcookie/cookies-plain.db, mode 0600.
func SidecarCookiesDB() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentcookie", "cookies-plain.db")
}
