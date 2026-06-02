package chrome

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultBrowserName    = "chrome"
	defaultBrowserProfile = "Default"
)

// Browser describes the Chromium-family source browser surfaces that vary by
// fork: the on-disk profile root and the Safe Storage keychain item.
type Browser struct {
	Name            string
	SupportDir      []string
	KeychainAccount string
	KeychainService string
}

var browserRegistry = map[string]Browser{
	defaultBrowserName: {
		Name:            defaultBrowserName,
		SupportDir:      []string{"Google", "Chrome"},
		KeychainAccount: keychainAccount,
		KeychainService: keychainService,
	},
	"atlas": {
		Name:       "atlas",
		SupportDir: []string{"OpenAI", "Atlas"},
		// UNVERIFIED: Atlas paths/keychain strings are best-known from
		// Chromium-fork conventions (Brave="Brave Safe Storage", Edge=
		// "Microsoft Edge Safe Storage"). Confirm against a real Atlas
		// install before relying on the atlas adapter. The doctor check
		// surfaces a wrong path/keychain as a loud failure rather than a
		// silent miss.
		KeychainAccount: "Atlas",
		KeychainService: "Atlas Safe Storage",
	},
}

// LookupBrowser returns the browser descriptor for name. Empty name defaults
// to Chrome for backward compatibility.
func LookupBrowser(name string) (Browser, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = defaultBrowserName
	}
	b, ok := browserRegistry[key]
	if !ok {
		return Browser{}, fmt.Errorf("unsupported browser %q (supported: %s)", name, strings.Join(SupportedBrowserNames(), ", "))
	}
	b.SupportDir = append([]string(nil), b.SupportDir...)
	return b, nil
}

// SupportedBrowserNames returns the configured source-browser adapter names.
func SupportedBrowserNames() []string {
	names := make([]string, 0, len(browserRegistry))
	for name := range browserRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ProfileDir returns this browser's profile directory. Empty profile defaults
// to "Default".
func (b Browser) ProfileDir(profile string) string {
	if profile == "" {
		profile = defaultBrowserProfile
	}
	home, _ := os.UserHomeDir()
	parts := []string{home, "Library", "Application Support"}
	parts = append(parts, b.SupportDir...)
	parts = append(parts, profile)
	return filepath.Join(parts...)
}

// CookiesPath returns this browser's Cookies SQLite path for profile. Empty
// profile defaults to "Default".
func (b Browser) CookiesPath(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "Cookies")
}

// LocalStorageLevelDB returns this browser's Local Storage LevelDB directory
// for profile. Empty profile defaults to "Default".
func (b Browser) LocalStorageLevelDB(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "Local Storage", "leveldb")
}

// IndexedDBDir returns this browser's IndexedDB directory for profile. Empty
// profile defaults to "Default".
func (b Browser) IndexedDBDir(profile string) string {
	return filepath.Join(b.ProfileDir(profile), "IndexedDB")
}
