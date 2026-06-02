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
		// DECRYPTION IS ARCHITECTURALLY UNSUPPORTED for Atlas via this adapter.
		// Cookies are tagged v10 (standard Chromium AES-128-CBC shape on macOS),
		// but Atlas does NOT use the legacy file-based "<App> Safe Storage"
		// keychain item that Chrome uses (and that agentcookie reads). On the
		// inspected build there is no such item in the login or System keychain,
		// no os_crypt/encrypted_key in Local State, and the v10 ciphertext does
		// not decrypt with the Chrome Safe Storage key or Chromium's "peanuts"
		// fallback.
		//
		// Root cause: Atlas (signed by OpenAI OpCo, team 2DC432GLL2) declares
		// keychain-access-groups ["2DC432GLL2.com.openai.shared"] and stores its
		// cookie key in the macOS DATA-PROTECTION keychain under that access
		// group. Data-protection keychain items are gated by code signature +
		// access group, so only a binary signed by OpenAI's team with that group
		// can read the key. No third-party tool (agentcookie included) can reach
		// it from the SQLite-path side -- this is a deliberate hardening away
		// from the world-readable Safe Storage model, not a missing-config bug.
		//
		// Path discovery still works (set browser.profile to your user-<uuid>
		// dir), and the doctor source-adapter check reports the decryption gap
		// loudly. A working Atlas integration would need a CDP/remote-debugging
		// path (read cookies through a running Atlas), which is out of scope for
		// this path-based adapter. These keychain fields are inert placeholders;
		// with no matching file-keychain item SafeStoragePasswordFor fails by
		// design. See issue #80.
		KeychainAccount: "Atlas",
		KeychainService: "Atlas Safe Storage",
	},
	// Brave / Edge / Arc use the standard file-based "<App> Safe Storage"
	// keychain model that Chrome uses (unlike Atlas), so the path + keychain
	// adapter applies directly. Profile paths verified on disk on 2026-06-02;
	// keychain account/service follow the well-established macOS convention
	// (browser_cookie3 / kooky / pycookiecheat use the same strings). Cookie
	// decryption was not exercised on the build machine (none were logged in,
	// so no Safe Storage item existed yet); the doctor source-adapter check
	// verifies decryption at runtime. Default profile is "Default" for all.
	"brave": {
		Name:            "brave",
		SupportDir:      []string{"BraveSoftware", "Brave-Browser"},
		KeychainAccount: "Brave",
		KeychainService: "Brave Safe Storage",
	},
	"edge": {
		Name:            "edge",
		SupportDir:      []string{"Microsoft Edge"},
		KeychainAccount: "Microsoft Edge",
		KeychainService: "Microsoft Edge Safe Storage",
	},
	"arc": {
		Name: "arc",
		// Arc inserts a "User Data" layer between the support dir and the
		// profile: ~/Library/Application Support/Arc/User Data/<profile>/.
		SupportDir:      []string{"Arc", "User Data"},
		KeychainAccount: "Arc",
		KeychainService: "Arc Safe Storage",
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
