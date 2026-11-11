package chrome_test

import (
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
)

// These literal expectations freeze the pre-registry adapters, rather than
// deriving both sides of the comparison from the new shared table.
func TestBrowserRegistryPreservesExistingAdapters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "ignored-xdg"))
	cases := []struct {
		name             string
		mac, linux       []string
		account, service string
	}{
		{"arc", []string{"Arc", "User Data"}, []string{"Arc", "User Data"}, "Arc", "Arc Safe Storage"},
		{"atlas", []string{"com.openai.atlas", "browser-data", "host"}, []string{"com.openai.atlas", "browser-data", "host"}, "Atlas", "Atlas Safe Storage"},
		{"brave", []string{"BraveSoftware", "Brave-Browser"}, []string{"BraveSoftware", "Brave-Browser"}, "Brave", "Brave Safe Storage"},
		{"chrome", []string{"Google", "Chrome"}, []string{"google-chrome"}, "Chrome", "Chrome Safe Storage"},
		{"dia", []string{"Dia", "User Data"}, []string{"Dia", "User Data"}, "Dia", "Dia Safe Storage"},
		{"edge", []string{"Microsoft Edge"}, []string{"microsoft-edge"}, "Microsoft Edge", "Microsoft Edge Safe Storage"},
	}
	names := []string{"arc", "atlas", "brave", "chrome", "dia", "edge"}
	if got := chrome.SupportedBrowserNames(); !reflect.DeepEqual(got, names) {
		t.Fatalf("chrome names = %v, want %v", got, names)
	}
	allNames := []string{"arc", "atlas", "brave", "chrome", "dia", "edge", "firefox", "floorp", "librewolf", "zen"}
	if got := config.SupportedBrowserNames(); !reflect.DeepEqual(got, allNames) {
		t.Fatalf("config names = %v, want %v", got, allNames)
	}
	for _, goos := range []string{"", "linux", "darwin", "windows"} {
		t.Run("GOOS="+goos, func(t *testing.T) {
			t.Setenv("GOOS", goos)
			for _, tc := range cases {
				for _, profile := range []string{"", "Default", "Profile 1", "user-test-uuid"} {
					b, err := chrome.LookupBrowser("  " + strings.ToUpper(tc.name) + "  ")
					if err != nil {
						t.Fatal(err)
					}
					if b.Name != tc.name || !reflect.DeepEqual(b.SupportDir, tc.mac) || b.KeychainAccount != tc.account || b.KeychainService != tc.service {
						t.Errorf("%s descriptor = %+v", tc.name, b)
					}
					resolvedProfile := profile
					if resolvedProfile == "" {
						resolvedProfile = "Default"
					}
					pathFor := func(linux bool) string {
						parts := []string{home, "Library", "Application Support"}
						dirs := tc.mac
						if linux {
							parts = []string{home, ".config"}
							dirs = tc.linux
						}
						return filepath.Join(append(append(parts, dirs...), resolvedProfile)...)
					}
					want := pathFor(goos == "linux" || (goos == "" && runtime.GOOS == "linux"))
					if got := b.ProfileDir(profile); got != want {
						t.Errorf("%s profile = %q, want %q", tc.name, got, want)
					}
					for label, pair := range map[string][2]string{
						"cookies":       {b.CookiesPath(profile), filepath.Join(want, "Cookies")},
						"local storage": {b.LocalStorageLevelDB(profile), filepath.Join(want, "Local Storage", "leveldb")},
						"indexed db":    {b.IndexedDBDir(profile), filepath.Join(want, "IndexedDB")},
					} {
						if pair[0] != pair[1] {
							t.Errorf("%s %s = %q, want %q", tc.name, label, pair[0], pair[1])
						}
					}
					// Config deliberately follows the compiled OS, not GOOS in
					// the environment. Chrome's historical override is retained.
					configPath, err := config.SourceBrowserCookiesPath(tc.name, profile)
					if err != nil {
						t.Fatal(err)
					}
					configWant := filepath.Join(pathFor(runtime.GOOS == "linux"), "Cookies")
					if configPath != configWant {
						t.Errorf("%s config = %q, want %q", tc.name, configPath, configWant)
					}
				}
			}
		})
	}
}

func TestBrowserLookupCompatibility(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"", " \t", "chrome", " CHROME "} {
		got, err := chrome.LookupBrowser(name)
		if err != nil || got.Name != "chrome" {
			t.Fatalf("chrome.LookupBrowser(%q) = %+v, %v", name, got, err)
		}
		path, err := config.SourceBrowserCookiesPath(name, "")
		if err != nil || path != config.DefaultChromeCookiesPath() {
			t.Fatalf("config default(%q) = %q, %v", name, path, err)
		}
	}
	const bad = " Unknown "
	const want = "unsupported browser \" Unknown \" (supported: arc, atlas, brave, chrome, dia, edge)"
	if _, err := chrome.LookupBrowser(bad); err == nil || err.Error() != want {
		t.Errorf("chrome error = %v, want %s", err, want)
	}
	const wantConfig = "unsupported browser \" Unknown \" (supported: arc, atlas, brave, chrome, dia, edge, firefox, floorp, librewolf, zen)"
	if _, err := config.SourceBrowserCookiesPath(bad, ""); err == nil || err.Error() != wantConfig {
		t.Errorf("config error = %v, want %s", err, wantConfig)
	}
	b, _ := chrome.LookupBrowser("chrome")
	b.SupportDir[0] = "changed"
	names := chrome.SupportedBrowserNames()
	names[0] = "changed"
	again, _ := chrome.LookupBrowser("chrome")
	if again.SupportDir[0] != "Google" || chrome.SupportedBrowserNames()[0] != "arc" {
		t.Fatal("caller mutation changed registry")
	}
}

func TestBrowserMissingHomeCompatibility(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("HOME controls UserHomeDir on Unix")
	}
	t.Setenv("HOME", "")
	t.Setenv("GOOS", "linux")
	b, err := chrome.LookupBrowser("")
	if err != nil {
		t.Fatal(err)
	}
	// The historical string-returning Chromium path helper ignores home errors.
	if got := b.CookiesPath(""); got != filepath.Join(".config", "google-chrome", "Default", "Cookies") {
		t.Errorf("chrome missing HOME = %q", got)
	}
	if _, err := config.SourceBrowserCookiesPath("", ""); err == nil {
		t.Fatal("config must continue returning missing HOME errors")
	}
	if got := config.DefaultChromeCookiesPath(); got != "" {
		t.Errorf("default config missing HOME = %q", got)
	}
}
