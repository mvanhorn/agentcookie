// Package gecko reads cookies from Firefox-family browsers (Zen, Firefox,
// LibreWolf, Floorp). Gecko stores cookies unencrypted in a profile's
// cookies.sqlite, so no Keychain item is involved. The reader only ever
// opens a private copy of that file; the live profile is never written.
package gecko

import (
	"path/filepath"
	"sort"
	"strings"
)

// CookiesFile is the cookie database file name inside a Gecko profile.
const CookiesFile = "cookies.sqlite"

// Browser describes where one Gecko browser keeps its profiles.
type Browser struct {
	Name string
	// MacRoot is the profile root under ~/Library/Application Support.
	MacRoot string
	// LinuxRoots are profile roots relative to the home directory: the
	// native root first, then its snap and flatpak roots.
	LinuxRoots []string
}

var browsers = map[string]Browser{
	"zen": {
		Name:       "zen",
		MacRoot:    "zen",
		LinuxRoots: []string{".zen", ".var/app/app.zen_browser.zen/.zen"},
	},
	"firefox": {
		Name:    "firefox",
		MacRoot: "Firefox",
		LinuxRoots: []string{
			".mozilla/firefox",
			"snap/firefox/common/.mozilla/firefox",
			".var/app/org.mozilla.firefox/.mozilla/firefox",
		},
	},
	"librewolf": {
		Name:       "librewolf",
		MacRoot:    "librewolf",
		LinuxRoots: []string{".librewolf", ".var/app/io.gitlab.librewolf-community/.librewolf"},
	},
	"floorp": {
		Name:       "floorp",
		MacRoot:    "Floorp",
		LinuxRoots: []string{".floorp", ".var/app/one.ablaze.floorp/.floorp"},
	},
}

// Lookup returns the Gecko browser called name, ignoring case and
// surrounding space.
func Lookup(name string) (Browser, bool) {
	b, ok := browsers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Browser{}, false
	}
	b.LinuxRoots = append([]string(nil), b.LinuxRoots...)
	return b, true
}

// IsBrowser reports whether name is a Gecko browser.
func IsBrowser(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// Names returns the Gecko browser names, sorted.
func Names() []string {
	names := make([]string, 0, len(browsers))
	for name := range browsers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Roots returns the absolute profile roots b may use under home.
func (b Browser) Roots(home string, linux bool) []string {
	if !linux {
		return []string{filepath.Join(home, "Library", "Application Support", b.MacRoot)}
	}
	roots := make([]string, 0, len(b.LinuxRoots))
	for _, r := range b.LinuxRoots {
		roots = append(roots, filepath.Join(home, filepath.FromSlash(r)))
	}
	return roots
}
