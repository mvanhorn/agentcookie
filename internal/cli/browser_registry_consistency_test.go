package cli

import (
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
)

// Chrome and config are views of internal/browsers. Keep these public-path
// agreement guards so a future change to either view cannot silently select
// a different profile or omit a supported Chromium adapter.

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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOOS", "")
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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOOS", "")
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
