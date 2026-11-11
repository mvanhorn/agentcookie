package chrome

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/browsers"
)

const (
	defaultBrowserName    = browsers.DefaultName
	defaultBrowserProfile = browsers.DefaultProfile
)

// Browser describes the Chromium-family source browser surfaces that vary by
// fork: the on-disk profile root and the Safe Storage keychain item.
type Browser struct {
	Name            string
	SupportDir      []string
	KeychainAccount string
	KeychainService string
}

// LookupBrowser returns the Chromium browser descriptor for name. Empty name
// defaults to Chrome for backward compatibility.
func LookupBrowser(name string) (Browser, error) {
	b, err := browsers.Lookup(name)
	if err != nil || b.Family != browsers.Chromium {
		return Browser{}, fmt.Errorf("unsupported browser %q (supported: %s)", name, strings.Join(SupportedBrowserNames(), ", "))
	}
	return Browser{
		Name:            b.Name,
		SupportDir:      b.SupportDir,
		KeychainAccount: b.KeychainAccount,
		KeychainService: b.KeychainService,
	}, nil
}

// SupportedBrowserNames returns the configured Chromium source-browser names.
func SupportedBrowserNames() []string {
	return browsers.SupportedNames(browsers.Chromium)
}

// ProfileDir returns this browser's profile directory. Empty profile defaults
// to "Default".
// On macOS: ~/Library/Application Support/<browser>/<profile>
// On Linux: ~/.config/<browser>/<profile>
func (b Browser) ProfileDir(profile string) string {
	if profile == "" {
		profile = defaultBrowserProfile
	}
	home, _ := os.UserHomeDir()
	var parts []string
	if isLinux() {
		parts = []string{home, ".config"}
		parts = append(parts, linuxSupportDir(b.SupportDir)...)
	} else {
		parts = []string{home, "Library", "Application Support"}
		parts = append(parts, b.SupportDir...)
	}
	parts = append(parts, profile)
	return filepath.Join(parts...)
}

// isLinux reports whether the current OS is Linux.
func isLinux() bool {
	return os.Getenv("GOOS") == "linux" || (os.Getenv("GOOS") == "" && runtime.GOOS == "linux")
}

// linuxSupportDir retains the path helper used by Chromium callers and tests.
func linuxSupportDir(macDirs []string) []string {
	return browsers.LinuxSupportDir(macDirs)
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
