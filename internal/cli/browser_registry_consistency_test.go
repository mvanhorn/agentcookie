package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
)

// The source-browser adapter table is intentionally maintained in two places:
// internal/chrome (which derives the Cookies path AND owns the Safe Storage
// keychain strings) and internal/config (which derives the Cookies path while
// staying free of the chrome package's CGO sqlite dependency). The two tables
// must agree, or a source.yaml `browser:` block could resolve a Cookies path
// the decryptor was never told about. These guards fail loudly the moment a
// new fork is added to one table but not the other.
//
// The cleaner long-term shape is a single dependency-free registry (e.g. in
// internal/chromepaths) consumed by both packages; until that refactor lands
// these tests are the drift backstop.

func TestSourceBrowserRegistriesAgreeOnSupportedNames(t *testing.T) {
	want := append(chrome.SupportedBrowserNames(), gecko.Names()...)
	sort.Strings(want)
	configNames := config.SupportedBrowserNames()

	if !slices.Equal(want, configNames) {
		t.Fatalf("config names %v, want chrome+gecko %v", configNames, want)
	}
	for _, name := range gecko.Names() {
		if _, err := chrome.LookupBrowser(name); err == nil {
			t.Errorf("%q is in both the Chromium and the Gecko table", name)
		}
	}
}

func TestSourceBrowserRegistriesAgreeOnCookiesPath(t *testing.T) {
	for _, name := range chrome.SupportedBrowserNames() {
		b, err := chrome.LookupBrowser(name)
		if err != nil {
			t.Fatalf("chrome.LookupBrowser(%q): %v", name, err)
		}
		// Both sides default an empty profile to "Default"; pass "" to each
		// so the comparison also covers that defaulting path.
		want := b.CookiesPath("")

		got, err := config.SourceBrowserCookiesPath(name, "")
		if err != nil {
			t.Fatalf("config.SourceBrowserCookiesPath(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("Cookies path drift for %q: chrome=%q config=%q", name, want, got)
		}
	}
}

func TestGeckoRegistryResolvesCookiesUnderItsRoot(t *testing.T) {
	for _, name := range gecko.Names() {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			b, _ := gecko.Lookup(name)
			root := b.Roots(home, runtime.GOOS == "linux")[0]
			if err := os.MkdirAll(filepath.Join(root, "p1.default"), 0o700); err != nil {
				t.Fatal(err)
			}
			ini := "[Profile0]\nName=default\nIsRelative=1\nPath=p1.default\n"
			if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte(ini), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := config.SourceBrowserCookiesPath(name, "")
			if err != nil {
				t.Fatalf("config.SourceBrowserCookiesPath(%q): %v", name, err)
			}
			if want := filepath.Join(root, "p1.default", "cookies.sqlite"); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// The chrome (default) adapter must resolve the same Default-profile surfaces
// as internal/chromepaths, which the sink side and other chrome-specific call
// sites still use directly. If the chrome registry entry's SupportDir is ever
// changed without updating chromepaths (or vice versa), the source adapter and
// the rest of the chrome-default code would silently disagree on where the
// profile lives.
func TestChromeAdapterMatchesChromepaths(t *testing.T) {
	chromeBrowser, err := chrome.LookupBrowser("chrome")
	if err != nil {
		t.Fatalf("chrome.LookupBrowser(\"chrome\"): %v", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"cookies", chromeBrowser.CookiesPath(""), chromepaths.CookiesDB()},
		{"localStorage", chromeBrowser.LocalStorageLevelDB(""), chromepaths.LocalStorageLevelDB()},
		{"indexedDB", chromeBrowser.IndexedDBDir(""), chromepaths.IndexedDBDir()},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("chrome adapter %s path drift: adapter=%q chromepaths=%q", tc.name, tc.got, tc.want)
		}
	}
}
