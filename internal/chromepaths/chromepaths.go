// Package chromepaths centralizes the on-disk Chrome profile paths
// agentcookie reads and writes. Supports macOS and Linux.
//
// v0.7 scope: Default profile only. v0.8+ discovers extra profiles
// (Profile N, Guest Profile, agent user-data-dirs) via Discover().
package chromepaths

import (
	"os"
	"path/filepath"
	"runtime"
)

// ChromeProfileRoot returns the user's Chrome user-data-dir on the current OS.
// On macOS: ~/Library/Application Support/Google/Chrome
// On Linux: ~/.config/google-chrome
func ChromeProfileRoot() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "google-chrome")
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// MacChromeProfileRoot returns the user's Chrome user-data-dir on macOS:
//
//	~/Library/Application Support/Google/Chrome
//
// Deprecated: Use ChromeProfileRoot() which is OS-aware.
func MacChromeProfileRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// DefaultProfileDir returns the path to the Default profile dir.
func DefaultProfileDir() string {
	return filepath.Join(ChromeProfileRoot(), "Default")
}

// CookiesDB returns the SQLite path for the Default profile's cookies.
// Checks Network/Cookies first (Chrome 96+), then falls back to Cookies.
func CookiesDB() string {
	return ProfileCookiesDB(DefaultProfileDir())
}

// ProfileCookiesDB returns the cookies SQLite path inside profileDir:
// Network/Cookies when it exists (Chrome 96+), else Cookies.
func ProfileCookiesDB(profileDir string) string {
	networkCookies := filepath.Join(profileDir, "Network", "Cookies")
	if info, err := os.Stat(networkCookies); err == nil && !info.IsDir() {
		return networkCookies
	}
	return filepath.Join(profileDir, "Cookies")
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
