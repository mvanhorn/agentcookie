// Package gecko reads cookies from Firefox-family browsers. The reader only
// ever opens a private copy of a profile's cookies.sqlite; the live profile
// is never written.
package gecko

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const CookiesFile = "cookies.sqlite"

// xdgConfigPrefix marks a Linux root under the XDG config directory:
// $XDG_CONFIG_HOME when it is set to an absolute path, else ~/.config.
const xdgConfigPrefix = ".config/"

type Browser struct {
	Name       string
	MacRoot    string
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
			// Current Firefox uses the XDG location when no legacy
			// ~/.mozilla exists.
			xdgConfigPrefix + "mozilla/firefox",
			".var/app/org.mozilla.firefox/config/mozilla/firefox",
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

func IsBrowser(name string) bool {
	_, ok := browsers[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func Names() []string {
	names := make([]string, 0, len(browsers))
	for name := range browsers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (b Browser) Roots(home string, linux bool) []string {
	if !linux {
		return []string{filepath.Join(home, "Library", "Application Support", b.MacRoot)}
	}
	roots := make([]string, 0, len(b.LinuxRoots))
	xdg := os.Getenv("XDG_CONFIG_HOME")
	for _, r := range b.LinuxRoots {
		if rest, ok := strings.CutPrefix(r, xdgConfigPrefix); ok && filepath.IsAbs(xdg) {
			roots = append(roots, filepath.Join(xdg, filepath.FromSlash(rest)))
			continue
		}
		roots = append(roots, filepath.Join(home, filepath.FromSlash(r)))
	}
	return roots
}
