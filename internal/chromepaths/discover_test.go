package chromepaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeProfile creates a minimal Chrome profile directory structure.
// If withCookies is true, creates an empty Cookies file.
// If networkLayout is true, puts Cookies in Network/Cookies.
func makeProfile(t *testing.T, root, profileName string, withCookies, networkLayout bool) string {
	t.Helper()
	profileDir := filepath.Join(root, profileName)
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile %s: %v", profileDir, err)
	}

	if withCookies {
		var cookiesPath string
		if networkLayout {
			networkDir := filepath.Join(profileDir, "Network")
			if err := os.MkdirAll(networkDir, 0o755); err != nil {
				t.Fatalf("mkdir network: %v", err)
			}
			cookiesPath = filepath.Join(networkDir, "Cookies")
		} else {
			cookiesPath = filepath.Join(profileDir, "Cookies")
		}
		if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
			t.Fatalf("write cookies: %v", err)
		}
	}

	return profileDir
}

func TestDiscover_DefaultWithCookies(t *testing.T) {
	// Create a fake Chrome root with a Default profile that has Cookies.
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	makeProfile(t, chromeRoot, "Default", true, false)

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected at least one store, got none")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" && s.IsDefault && s.Browser == "chrome" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile not found or not marked as default")
	}
}

func TestDiscover_NoCookiesFile_Skipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile without Cookies file.
	makeProfile(t, chromeRoot, "Default", false, false)

	result := Discover()

	if len(result.Stores) != 0 {
		t.Errorf("expected no stores without Cookies file, got %d", len(result.Stores))
	}

	// Should be in Skipped list.
	found := false
	for _, s := range result.Skipped {
		if s.Profile == "Default" && s.Reason == "no Cookies file" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile should be in Skipped list with 'no Cookies file' reason")
	}
}

func TestDiscover_NetworkCookiesLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile with Network/Cookies layout.
	makeProfile(t, chromeRoot, "Default", true, true)

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected at least one store with Network/Cookies, got none")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" {
			if filepath.Base(filepath.Dir(s.CookiesPath)) != "Network" {
				t.Errorf("expected Network/Cookies path, got %s", s.CookiesPath)
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile not found")
	}
}

func TestDiscover_MultipleProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create multiple profiles.
	makeProfile(t, chromeRoot, "Default", true, false)
	makeProfile(t, chromeRoot, "Profile 1", true, true) // Network/Cookies
	makeProfile(t, chromeRoot, "Profile 2", true, false)
	makeProfile(t, chromeRoot, "Guest Profile", true, false)

	result := Discover()

	if len(result.Stores) != 4 {
		t.Errorf("expected 4 stores, got %d", len(result.Stores))
	}

	profiles := make(map[string]bool)
	for _, s := range result.Stores {
		profiles[s.Profile] = true
	}

	for _, want := range []string{"Default", "Profile 1", "Profile 2", "Guest Profile"} {
		if !profiles[want] {
			t.Errorf("missing profile: %s", want)
		}
	}
}

func TestDiscover_SkipsCacheDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Crashpad and ShaderCache dirs (should be skipped).
	makeProfile(t, chromeRoot, "Crashpad", true, false)
	makeProfile(t, chromeRoot, "ShaderCache", true, false)
	makeProfile(t, chromeRoot, "GPUCache", true, false)
	// Also create a valid profile.
	makeProfile(t, chromeRoot, "Default", true, false)

	result := Discover()

	// Should only find Default.
	if len(result.Stores) != 1 {
		t.Errorf("expected 1 store (only Default), got %d", len(result.Stores))
	}
	if len(result.Stores) > 0 && result.Stores[0].Profile != "Default" {
		t.Errorf("expected Default profile, got %s", result.Stores[0].Profile)
	}

	// Cache dirs should not be in Skipped list either (they're just ignored).
	for _, s := range result.Skipped {
		if s.Profile == "Crashpad" || s.Profile == "ShaderCache" || s.Profile == "GPUCache" {
			t.Errorf("cache dir %s should not be in Skipped list", s.Profile)
		}
	}
}

func TestDiscover_AgentChromeProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create agent chrome-profile directory (the directory itself is the profile).
	chromeProfile := filepath.Join(home, "chrome-profile")
	if err := os.MkdirAll(chromeProfile, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(chromeProfile, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := Discover()

	found := false
	for _, s := range result.Stores {
		if s.Profile == "chrome-profile" && !s.IsDefault {
			found = true
			break
		}
	}
	if !found {
		t.Error("~/chrome-profile should be discovered as a store")
	}
}

func TestDiscover_AgentCookieChromeProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create ~/.agentcookie/chrome-profile directory.
	chromeProfile := filepath.Join(home, ".agentcookie", "chrome-profile")
	if err := os.MkdirAll(chromeProfile, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(chromeProfile, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := Discover()

	found := false
	for _, s := range result.Stores {
		if s.Profile == "chrome-profile" && !s.IsDefault {
			found = true
			break
		}
	}
	if !found {
		t.Error("~/.agentcookie/chrome-profile should be discovered as a store")
	}
}

func TestDiscover_ChromeUserDataDirEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a custom user-data-dir.
	customRoot := filepath.Join(t.TempDir(), "my-custom-chrome")
	makeProfile(t, customRoot, "Default", true, false)

	t.Setenv("CHROME_USER_DATA_DIR", customRoot)

	result := Discover()

	found := false
	for _, s := range result.Stores {
		if s.Root == customRoot && s.Profile == "Default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("CHROME_USER_DATA_DIR profile should be discovered")
	}
}

func TestDiscover_NoLocalStateRequired(t *testing.T) {
	// AE1: Fixture root with Default/Cookies and no Local State is discovered.
	home := t.TempDir()
	t.Setenv("HOME", home)

	var chromeRoot string
	if runtime.GOOS == "darwin" {
		chromeRoot = filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	} else {
		chromeRoot = filepath.Join(home, ".config", "google-chrome")
	}
	if err := os.MkdirAll(chromeRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create Default profile with Cookies but NO Local State.
	makeProfile(t, chromeRoot, "Default", true, false)

	// Verify no Local State exists.
	localStatePath := filepath.Join(chromeRoot, "Local State")
	if _, err := os.Stat(localStatePath); err == nil {
		t.Fatal("Local State should not exist for this test")
	}

	result := Discover()

	if len(result.Stores) == 0 {
		t.Fatal("expected store to be discovered without Local State")
	}

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Default profile should be discovered even without Local State")
	}
}

func TestDiscoverForConfig_AddsExplicitProfileDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create an explicit profile dir not in the standard locations.
	explicitDir := filepath.Join(t.TempDir(), "explicit-profile")
	if err := os.MkdirAll(explicitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(explicitDir, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	result := DiscoverForConfig(explicitDir)

	found := false
	for _, s := range result.Stores {
		if s.CookiesPath == cookiesPath {
			found = true
			break
		}
	}
	if !found {
		t.Error("explicit profile dir should be in discovery result")
	}
}

// TestDiscoverForConfig_ScansUserDataDir verifies that when cdp.profile_dir
// is a Chrome user-data-dir (containing Default/Profile N), DiscoverForConfig
// scans those children the same way Discover scans standard roots.
func TestDiscoverForConfig_ScansUserDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a user-data-dir with Default and Profile 1 subdirs (no Local State).
	userDataDir := filepath.Join(t.TempDir(), "my-chrome-data")

	// Default profile with Cookies.
	defaultDir := filepath.Join(userDataDir, "Default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defaultCookies := filepath.Join(defaultDir, "Cookies")
	if err := os.WriteFile(defaultCookies, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Profile 1 with Network/Cookies.
	profile1Dir := filepath.Join(userDataDir, "Profile 1")
	networkDir := filepath.Join(profile1Dir, "Network")
	if err := os.MkdirAll(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile1Cookies := filepath.Join(networkDir, "Cookies")
	if err := os.WriteFile(profile1Cookies, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a cache dir that should be skipped.
	cacheDir := filepath.Join(userDataDir, "Cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	result := DiscoverForConfig(userDataDir)

	// Should find both Default and Profile 1.
	foundDefault := false
	foundProfile1 := false
	for _, s := range result.Stores {
		if s.Root == userDataDir && s.Profile == "Default" && s.CookiesPath == defaultCookies {
			foundDefault = true
			if !s.IsDefault {
				t.Error("Default profile should have IsDefault=true")
			}
		}
		if s.Root == userDataDir && s.Profile == "Profile 1" && s.CookiesPath == profile1Cookies {
			foundProfile1 = true
		}
	}
	if !foundDefault {
		t.Error("Default profile should be discovered from user-data-dir")
	}
	if !foundProfile1 {
		t.Error("Profile 1 should be discovered from user-data-dir")
	}
}

// TestDiscoverForConfig_ExpandsTilde verifies that DiscoverForConfig expands
// ~ to the home directory. filepath.Abs treats ~ as a literal path component,
// so we must expand it first.
func TestDiscoverForConfig_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a user-data-dir under HOME with Default/Cookies.
	chromeProfile := filepath.Join(home, "chrome-profile")
	defaultDir := filepath.Join(chromeProfile, "Default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cookiesPath := filepath.Join(defaultDir, "Cookies")
	if err := os.WriteFile(cookiesPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	// Use tilde shorthand - this should expand to home/chrome-profile.
	result := DiscoverForConfig("~/chrome-profile")

	found := false
	for _, s := range result.Stores {
		if s.Root == chromeProfile && s.Profile == "Default" && s.CookiesPath == cookiesPath {
			found = true
			break
		}
	}
	if !found {
		t.Error("DiscoverForConfig(\"~/chrome-profile\") should find Default/Cookies under home")
	}
}

func TestProfileNameAllowlist(t *testing.T) {
	cases := []struct {
		name    string
		matches bool
	}{
		{"Default", true},
		{"Profile 1", true},
		{"Profile 2", true},
		{"Profile 10", true},
		{"Profile 123", true},
		{"Guest Profile", true},
		{"System Profile", true},
		{"Profile", false},        // Missing number
		{"Profile1", false},       // No space
		{"Profile X", false},      // Non-numeric
		{"Crashpad", false},       // Cache dir
		{"random-dir", false},     // Random
		{"Default ", false},       // Trailing space
		{" Default", false},       // Leading space
		{"Default123", false},     // Invalid
		{"MyProfile 1", false},    // Invalid prefix
		{"Guest", false},          // Incomplete
		{"System", false},         // Incomplete
		{"Guest Profile ", false}, // Trailing space
	}

	for _, tc := range cases {
		got := profileNameAllowlist.MatchString(tc.name)
		if got != tc.matches {
			t.Errorf("profileNameAllowlist.MatchString(%q) = %v, want %v", tc.name, got, tc.matches)
		}
	}
}

func TestBrowserForRoot(t *testing.T) {
	cases := []struct {
		root string
		want string
	}{
		{"/Users/me/Library/Application Support/Google/Chrome", "chrome"},
		{"/Users/me/Library/Application Support/Chromium", "chromium"},
		{"/Users/me/Library/Application Support/BraveSoftware/Brave-Browser", "brave"},
		{"/Users/me/Library/Application Support/Microsoft Edge", "edge"},
		{"/home/me/.config/google-chrome", "chrome"},
		{"/home/me/.config/chromium", "chromium"},
		{"/home/me/.config/BraveSoftware/Brave-Browser", "brave"},
		{"/home/me/.config/microsoft-edge", "edge"},
		{"/home/me/chrome-profile", "chrome"},
		{"/some/random/path", "chrome"}, // Default
		{"/Users/me/Library/Application Support/Dia/User Data", "dia"},
		{"/Users/me/Library/Application Support/some-media-app", "chrome"}, // "media" contains "dia"
	}

	for _, tc := range cases {
		got := browserForRoot(tc.root)
		if got != tc.want {
			t.Errorf("browserForRoot(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}

// TestDiscoverForConfig_DiaUserDataKeepsDiaIdentity is the #120 discovery
// guard: a Dia User Data root must stay labeled "dia" so key lookup uses
// Dia Safe Storage, not Chrome Safe Storage.
func TestDiscoverForConfig_DiaUserDataKeepsDiaIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	diaRoot := filepath.Join(home, "Library", "Application Support", "Dia", "User Data")
	if err := os.MkdirAll(diaRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	makeProfile(t, diaRoot, "Default", true, false)

	result := DiscoverForConfig(diaRoot)

	found := false
	for _, s := range result.Stores {
		if s.Profile == "Default" && s.Browser == "dia" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dia/Default store from Dia User Data root, got %+v", result.Stores)
	}
}
