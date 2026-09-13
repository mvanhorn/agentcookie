package chrome

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// browserTestBase returns the base config directory for browser tests.
// On macOS: ~/Library/Application Support
// On Linux: ~/.config
func browserTestBase(home string) string {
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config")
	}
	return filepath.Join(home, "Library", "Application Support")
}

// browserTestCookiesDir returns the expected cookies directory path for a browser.
// Handles the OS-specific path translation.
func browserTestCookiesDir(home string, macDirs []string, profile string) string {
	base := browserTestBase(home)
	if runtime.GOOS == "linux" {
		dirs := linuxSupportDir(macDirs)
		return filepath.Join(append(append([]string{base}, dirs...), profile, "Cookies")...)
	}
	return filepath.Join(append(append([]string{base}, macDirs...), profile, "Cookies")...)
}

func TestLookupBrowserDefaultsToChrome(t *testing.T) {
	b, err := LookupBrowser("")
	if err != nil {
		t.Fatalf("LookupBrowser(\"\"): %v", err)
	}
	if b.Name != "chrome" {
		t.Errorf("Name: got %q, want chrome", b.Name)
	}
	if b.KeychainAccount != "Chrome" || b.KeychainService != "Chrome Safe Storage" {
		t.Errorf("keychain: got account=%q service=%q", b.KeychainAccount, b.KeychainService)
	}
}

func TestLookupBrowserAtlas(t *testing.T) {
	b, err := LookupBrowser("atlas")
	if err != nil {
		t.Fatalf("LookupBrowser(atlas): %v", err)
	}
	if b.Name != "atlas" {
		t.Errorf("Name: got %q, want atlas", b.Name)
	}
	if b.KeychainAccount != "Atlas" || b.KeychainService != "Atlas Safe Storage" {
		t.Errorf("keychain: got account=%q service=%q", b.KeychainAccount, b.KeychainService)
	}
}

func TestLookupBrowserUnknownListsSupportedNames(t *testing.T) {
	_, err := LookupBrowser("vivaldi")
	if err == nil {
		t.Fatal("expected unsupported browser error")
	}
	if !strings.Contains(err.Error(), "supported:") || !strings.Contains(err.Error(), "chrome") {
		t.Errorf("error should list supported names, got %v", err)
	}
}

func TestLookupBrowserStandardForks(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name       string
		cookiesDir []string // path segments under Application Support (macOS), before profile
		account    string
		service    string
	}{
		{"brave", []string{"BraveSoftware", "Brave-Browser"}, "Brave", "Brave Safe Storage"},
		{"edge", []string{"Microsoft Edge"}, "Microsoft Edge", "Microsoft Edge Safe Storage"},
		{"arc", []string{"Arc", "User Data"}, "Arc", "Arc Safe Storage"},
		{"dia", []string{"Dia", "User Data"}, "Dia", "Dia Safe Storage"},
	}
	for _, tc := range cases {
		b, err := LookupBrowser(tc.name)
		if err != nil {
			t.Fatalf("LookupBrowser(%s): %v", tc.name, err)
		}
		if b.KeychainAccount != tc.account || b.KeychainService != tc.service {
			t.Errorf("%s keychain: got account=%q service=%q", tc.name, b.KeychainAccount, b.KeychainService)
		}
		wantCookies := browserTestCookiesDir(home, tc.cookiesDir, "Default")
		if got := b.CookiesPath(""); got != wantCookies {
			t.Errorf("%s cookies path: got %q, want %q", tc.name, got, wantCookies)
		}
	}
}

func TestBrowserCookiesPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	base := browserTestBase(home)

	chromeBrowser, err := LookupBrowser("chrome")
	if err != nil {
		t.Fatal(err)
	}

	// Chrome paths - OS-aware
	var chromePath, chromeProfileDir, chromeLocalStorage, chromeIndexedDB string
	if runtime.GOOS == "linux" {
		chromePath = filepath.Join(base, "google-chrome", "Default", "Cookies")
		chromeProfileDir = filepath.Join(base, "google-chrome", "Default")
		chromeLocalStorage = filepath.Join(base, "google-chrome", "Default", "Local Storage", "leveldb")
		chromeIndexedDB = filepath.Join(base, "google-chrome", "Default", "IndexedDB")
	} else {
		chromePath = filepath.Join(base, "Google", "Chrome", "Default", "Cookies")
		chromeProfileDir = filepath.Join(base, "Google", "Chrome", "Default")
		chromeLocalStorage = filepath.Join(base, "Google", "Chrome", "Default", "Local Storage", "leveldb")
		chromeIndexedDB = filepath.Join(base, "Google", "Chrome", "Default", "IndexedDB")
	}

	if got := chromeBrowser.CookiesPath(""); got != chromePath {
		t.Errorf("chrome default path: got %q, want %q", got, chromePath)
	}
	if got := chromeBrowser.ProfileDir(""); got != chromeProfileDir {
		t.Errorf("chrome default profile dir: got %q, want %q", got, chromeProfileDir)
	}
	if got := chromeBrowser.LocalStorageLevelDB(""); got != chromeLocalStorage {
		t.Errorf("chrome default local storage path: got %q, want %q", got, chromeLocalStorage)
	}
	if got := chromeBrowser.IndexedDBDir(""); got != chromeIndexedDB {
		t.Errorf("chrome default indexeddb path: got %q, want %q", got, chromeIndexedDB)
	}

	atlasBrowser, err := LookupBrowser("atlas")
	if err != nil {
		t.Fatal(err)
	}

	// Atlas paths - Atlas is macOS-only but the path helpers still work on Linux.
	// On Linux, the macOS path segments are used directly since Atlas doesn't have
	// a Linux-specific path mapping.
	var atlasPath, atlasProfileDir, atlasLocalStorage, atlasIndexedDB string
	if runtime.GOOS == "linux" {
		atlasPath = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "Cookies")
		atlasProfileDir = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1")
		atlasLocalStorage = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "Local Storage", "leveldb")
		atlasIndexedDB = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "IndexedDB")
	} else {
		atlasPath = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "Cookies")
		atlasProfileDir = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1")
		atlasLocalStorage = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "Local Storage", "leveldb")
		atlasIndexedDB = filepath.Join(base, "com.openai.atlas", "browser-data", "host", "Profile 1", "IndexedDB")
	}

	if got := atlasBrowser.CookiesPath("Profile 1"); got != atlasPath {
		t.Errorf("atlas profile path: got %q, want %q", got, atlasPath)
	}
	if got := atlasBrowser.ProfileDir("Profile 1"); got != atlasProfileDir {
		t.Errorf("atlas profile dir: got %q, want %q", got, atlasProfileDir)
	}
	if got := atlasBrowser.LocalStorageLevelDB("Profile 1"); got != atlasLocalStorage {
		t.Errorf("atlas local storage path: got %q, want %q", got, atlasLocalStorage)
	}
	if got := atlasBrowser.IndexedDBDir("Profile 1"); got != atlasIndexedDB {
		t.Errorf("atlas indexeddb path: got %q, want %q", got, atlasIndexedDB)
	}
}
