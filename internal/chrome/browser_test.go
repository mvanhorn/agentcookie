package chrome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	_, err := LookupBrowser("dia")
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
		cookiesDir []string // path segments under Application Support, before profile
		account    string
		service    string
	}{
		{"brave", []string{"BraveSoftware", "Brave-Browser"}, "Brave", "Brave Safe Storage"},
		{"edge", []string{"Microsoft Edge"}, "Microsoft Edge", "Microsoft Edge Safe Storage"},
		{"arc", []string{"Arc", "User Data"}, "Arc", "Arc Safe Storage"},
	}
	for _, tc := range cases {
		b, err := LookupBrowser(tc.name)
		if err != nil {
			t.Fatalf("LookupBrowser(%s): %v", tc.name, err)
		}
		if b.KeychainAccount != tc.account || b.KeychainService != tc.service {
			t.Errorf("%s keychain: got account=%q service=%q", tc.name, b.KeychainAccount, b.KeychainService)
		}
		base := append([]string{home, "Library", "Application Support"}, tc.cookiesDir...)
		wantCookies := filepath.Join(append(append([]string{}, base...), "Default", "Cookies")...)
		if got := b.CookiesPath(""); got != wantCookies {
			t.Errorf("%s cookies path: got %q, want %q", tc.name, got, wantCookies)
		}
	}
}

func TestBrowserCookiesPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	chromeBrowser, err := LookupBrowser("chrome")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := chromeBrowser.CookiesPath(""), filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Cookies"); got != want {
		t.Errorf("chrome default path: got %q, want %q", got, want)
	}
	if got, want := chromeBrowser.ProfileDir(""), filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default"); got != want {
		t.Errorf("chrome default profile dir: got %q, want %q", got, want)
	}
	if got, want := chromeBrowser.LocalStorageLevelDB(""), filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Local Storage", "leveldb"); got != want {
		t.Errorf("chrome default local storage path: got %q, want %q", got, want)
	}
	if got, want := chromeBrowser.IndexedDBDir(""), filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "IndexedDB"); got != want {
		t.Errorf("chrome default indexeddb path: got %q, want %q", got, want)
	}

	atlasBrowser, err := LookupBrowser("atlas")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := atlasBrowser.CookiesPath("Profile 1"), filepath.Join(home, "Library", "Application Support", "com.openai.atlas", "browser-data", "host", "Profile 1", "Cookies"); got != want {
		t.Errorf("atlas profile path: got %q, want %q", got, want)
	}
	if got, want := atlasBrowser.ProfileDir("Profile 1"), filepath.Join(home, "Library", "Application Support", "com.openai.atlas", "browser-data", "host", "Profile 1"); got != want {
		t.Errorf("atlas profile dir: got %q, want %q", got, want)
	}
	if got, want := atlasBrowser.LocalStorageLevelDB("Profile 1"), filepath.Join(home, "Library", "Application Support", "com.openai.atlas", "browser-data", "host", "Profile 1", "Local Storage", "leveldb"); got != want {
		t.Errorf("atlas local storage path: got %q, want %q", got, want)
	}
	if got, want := atlasBrowser.IndexedDBDir("Profile 1"), filepath.Join(home, "Library", "Application Support", "com.openai.atlas", "browser-data", "host", "Profile 1", "IndexedDB"); got != want {
		t.Errorf("atlas indexeddb path: got %q, want %q", got, want)
	}
}
