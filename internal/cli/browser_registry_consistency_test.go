package cli

import (
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
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
	chromeNames := chrome.SupportedBrowserNames()
	configNames := config.SupportedBrowserNames()

	if len(chromeNames) != len(configNames) {
		t.Fatalf("supported-browser sets differ in size: chrome=%v config=%v", chromeNames, configNames)
	}
	for i := range chromeNames {
		if chromeNames[i] != configNames[i] {
			t.Fatalf("supported-browser sets differ at %d: chrome=%v config=%v", i, chromeNames, configNames)
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
