package onboarding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChromeAppDoesNotLaunchOrOverwrite(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "Applications", "agentcookie Chrome.app")
	if err := InstallChromeApp(app, "/does/not/exist/agentcookie", root, "live_chrome"); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(app, "Contents/MacOS/launch"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "destination open 'live_chrome'") {
		t.Fatal(string(script))
	}
	if err := InstallChromeApp(app, "/different", root, "other"); err == nil {
		t.Fatal("overwrote bundle")
	}
}

func TestNamedChromeAppsHaveDistinctBundleIdentity(t *testing.T) {
	home := t.TempDir()
	var plists []string
	for _, name := range []string{"personal", "work"} {
		path := ChromeAppPath(home, name)
		if err := InstallChromeApp(path, "/fixture/agentcookie", home, name); err != nil {
			t.Fatal(err)
		}
		script, err := os.ReadFile(filepath.Join(path, "Contents/MacOS/launch"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(script), "destination open '"+name+"'") {
			t.Fatal("wrong destination")
		}
		plist, err := os.ReadFile(filepath.Join(path, "Contents/Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plist), "dev.agentcookie.chrome."+name) {
			t.Fatal("bundle identifier not isolated")
		}
		plists = append(plists, string(plist))
	}
	if plists[0] == plists[1] {
		t.Fatal("launcher identity collision")
	}
	if ChromeAppPath(home, "live_chrome") != filepath.Join(home, "Applications", "agentcookie Chrome.app") {
		t.Fatal("legacy path changed")
	}
}

func TestChromeAppUnderscoreNamesDoNotAliasBundleIDs(t *testing.T) {
	home := t.TempDir()
	ids := map[string]bool{}
	for _, name := range []string{"team_one", "team-one"} {
		app := ChromeAppPath(home, name)
		if err := InstallChromeApp(app, "/fixture/bin", home, name); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(app, "Contents/Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		_, rest, _ := strings.Cut(string(raw), "<key>CFBundleIdentifier</key><string>")
		id, _, _ := strings.Cut(rest, "</string>")
		if id == "" || strings.Contains(id, "_") || ids[id] {
			t.Fatalf("invalid or aliased bundle id %q", id)
		}
		ids[id] = true
	}
}
