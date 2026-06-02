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
		Name: "atlas",
		// VERIFIED against ChatGPT Atlas 149.0.7827.29 (bundle com.openai.atlas)
		// on 2026-06-02. Atlas is Chromium underneath but does NOT follow the
		// standard profile layout: the profile root is
		// ~/Library/Application Support/com.openai.atlas/browser-data/host/,
		// and profiles are UUID-scoped ("user-<uuid>", plus an empty "Default"
		// and a "login-staging-<uuid>"). The live session lives in the
		// user-<uuid> profile, so callers must set browser.profile to that dir
		// explicitly -- the "Default" default resolves to an empty Cookies DB.
		SupportDir: []string{"com.openai.atlas", "browser-data", "host"},
		// DECRYPTION IS NOT YET SUPPORTED for Atlas. Cookies are tagged v10
		// (standard Chromium AES-128-GCM shape), but on the inspected build
		// Atlas ships NO "<App> Safe Storage" keychain item (in either the
		// login or System keychain), carries no os_crypt key in Local State,
		// and its v10 ciphertext does not decrypt with the Chrome Safe Storage
		// key or Chromium's "peanuts" fallback. Atlas appears to use a custom
		// key provider whose key is not externally recoverable without further
		// work. These keychain fields are therefore placeholders: with no
		// matching item, SafeStoragePasswordFor fails and the doctor source-
		// adapter check reports the decryption gap loudly rather than silently
		// shipping undecryptable cookies. See issue #80.
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
