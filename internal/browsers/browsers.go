// Package browsers defines source-browser metadata without importing browser
// readers, configuration, CGO, or any third-party dependency.
package browsers

import (
	"fmt"
	"sort"
	"strings"
)

// Family identifies the browser engine used by a source adapter.
type Family string

const (
	Chromium       Family = "chromium"
	Gecko          Family = "gecko"
	DefaultName           = "chrome"
	DefaultProfile        = "Default"
)

// Browser describes a source browser. SupportDir contains the macOS
// Application Support path segments; LinuxSupportDir maps Chromium roots
// to their historical Linux equivalents. Keychain fields describe only the
// Chromium file-based Safe Storage adapter, not support for decryption.
type Browser struct {
	Name            string
	Family          Family
	SupportDir      []string
	KeychainAccount string
	KeychainService string
}

var browserRegistry = map[string]Browser{
	DefaultName: {
		Name:            DefaultName,
		Family:          Chromium,
		SupportDir:      []string{"Google", "Chrome"},
		KeychainAccount: "Chrome",
		KeychainService: "Chrome Safe Storage",
	},
	"atlas": {
		Name:   "atlas",
		Family: Chromium,
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
		Family:          Chromium,
		SupportDir:      []string{"BraveSoftware", "Brave-Browser"},
		KeychainAccount: "Brave",
		KeychainService: "Brave Safe Storage",
	},
	"edge": {
		Name:            "edge",
		Family:          Chromium,
		SupportDir:      []string{"Microsoft Edge"},
		KeychainAccount: "Microsoft Edge",
		KeychainService: "Microsoft Edge Safe Storage",
	},
	"arc": {
		Name:   "arc",
		Family: Chromium,
		// Arc inserts a "User Data" layer between the support dir and the
		// profile: ~/Library/Application Support/Arc/User Data/<profile>/.
		SupportDir:      []string{"Arc", "User Data"},
		KeychainAccount: "Arc",
		KeychainService: "Arc Safe Storage",
	},
	// Dia (The Browser Company) follows the same "User Data" layout as Arc.
	// Profile paths verified on disk 2026-08-24; keychain account/service
	// follow the standard macOS Chromium-fork convention.
	"dia": {
		Name:            "dia",
		Family:          Chromium,
		SupportDir:      []string{"Dia", "User Data"},
		KeychainAccount: "Dia",
		KeychainService: "Dia Safe Storage",
	},
}

// Lookup returns an independent descriptor. Names are case-insensitive and
// trimmed; an empty name selects Chrome for backward compatibility.
func Lookup(name string) (Browser, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = DefaultName
	}
	b, ok := browserRegistry[key]
	if !ok {
		return Browser{}, fmt.Errorf("unsupported browser %q (supported: %s)", name, strings.Join(SupportedNames(""), ", "))
	}
	b.SupportDir = append([]string(nil), b.SupportDir...)
	return b, nil
}

// SupportedNames returns sorted names for family, or every name when family
// is empty. The returned slice can be modified without changing the registry.
func SupportedNames(family Family) []string {
	names := make([]string, 0, len(browserRegistry))
	for name, b := range browserRegistry {
		if family == "" || b.Family == family {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// LinuxSupportDir converts macOS Application Support subdirectory names to
// their Linux ~/.config equivalents.
func LinuxSupportDir(macDirs []string) []string {
	if len(macDirs) == 0 {
		return macDirs
	}
	switch macDirs[0] {
	case "Google":
		if len(macDirs) >= 2 && macDirs[1] == "Chrome" {
			return []string{"google-chrome"}
		}
		return macDirs
	case "Chromium":
		return []string{"chromium"}
	case "BraveSoftware":
		return macDirs
	case "Microsoft Edge":
		return []string{"microsoft-edge"}
	case "Arc", "Dia":
		return macDirs // Arc and Dia are macOS-only
	default:
		return macDirs
	}
}
