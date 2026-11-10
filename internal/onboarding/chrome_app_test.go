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
