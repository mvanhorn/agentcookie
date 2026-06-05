package agentbrowser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempDir points the launcher dir at a temp dir for the duration of a
// test and restores the override afterward.
func withTempDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	prev := dirOverride
	dirOverride = d
	t.Cleanup(func() { dirOverride = prev })
	return d
}

func TestBrowserUse_Wire_WritesExecutableLauncher(t *testing.T) {
	dir := withTempDir(t)
	b := newBrowserUseAt("/usr/local/bin/browser-use")
	// A live ws endpoint is supplied, but the durable launcher must bake the
	// stable http://127.0.0.1:<port> form, not the per-session ws URL.
	wantURL := "http://127.0.0.1:9222"

	res, err := b.Wire(AttachTarget{Port: 9222, WSEndpoint: "ws://127.0.0.1:9222/devtools/browser/abc"})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	want := filepath.Join(dir, "browser-use-attached")
	if res.LauncherPath != want {
		t.Errorf("LauncherPath = %q, want %q", res.LauncherPath, want)
	}

	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat launcher: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("launcher not executable: mode %v", info.Mode())
	}

	body, _ := os.ReadFile(want)
	s := string(body)
	if !strings.Contains(s, "--cdp-url") || !strings.Contains(s, wantURL) {
		t.Errorf("launcher missing attach flag/stable endpoint:\n%s", s)
	}
	if strings.Contains(s, "devtools/browser/abc") {
		t.Errorf("launcher baked the per-session ws id; should use stable http endpoint:\n%s", s)
	}
	if !strings.Contains(s, "'/usr/local/bin/browser-use'") {
		t.Errorf("launcher does not exec the real binary:\n%s", s)
	}
	if !strings.Contains(s, `"$@"`) {
		t.Errorf("launcher does not forward args:\n%s", s)
	}
}

func TestBrowserUse_Wire_Idempotent(t *testing.T) {
	withTempDir(t)
	b := newBrowserUseAt("/usr/local/bin/browser-use")
	endpoint := "ws://127.0.0.1:9222/devtools/browser/abc"

	target := AttachTarget{Port: 9222, WSEndpoint: endpoint}
	r1, err := b.Wire(target)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(r1.LauncherPath)
	r2, err := b.Wire(target)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(r2.LauncherPath)
	if string(first) != string(second) {
		t.Errorf("re-wiring the same endpoint changed bytes:\n%s\n---\n%s", first, second)
	}
}

func TestBrowserUse_Wire_RejectsBadEndpoint(t *testing.T) {
	withTempDir(t)
	b := newBrowserUseAt("/usr/local/bin/browser-use")
	bad := []string{
		"",
		"localhost:9222",                  // no scheme
		"ws://127.0.0.1:9222/x; rm -rf /", // shell injection
		"ws://127.0.0.1:9222/$(whoami)",   // command substitution
		"ws://127.0.0.1:9222/\nmalicious", // newline
	}
	for _, e := range bad {
		if _, err := b.Wire(AttachTarget{WSEndpoint: e}); err == nil {
			t.Errorf("Wire(%q) succeeded, want rejection", e)
		}
	}
}

func TestBrowserUse_Wire_PortOnlyUsesHTTP(t *testing.T) {
	withTempDir(t)
	b := newBrowserUseAt("/usr/local/bin/browser-use")
	res, err := b.Wire(AttachTarget{Port: 9222})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	body, _ := os.ReadFile(res.LauncherPath)
	if !strings.Contains(string(body), "http://127.0.0.1:9222") {
		t.Errorf("port-only target should produce an http:// cdp-url:\n%s", body)
	}
}

func TestBrowserUse_LaunchSnippet(t *testing.T) {
	b := newBrowserUseAt("/usr/local/bin/browser-use")
	snip := b.LaunchSnippet(AttachTarget{Port: 9222, WSEndpoint: "ws://127.0.0.1:9222/devtools/browser/abc"})
	if !strings.Contains(snip, "--cdp-url") || !strings.Contains(snip, "http://127.0.0.1:9222") {
		t.Errorf("snippet missing attach: %q", snip)
	}
	if !strings.Contains(snip, "--connect") {
		t.Errorf("snippet should mention the --connect alternative: %q", snip)
	}
}

func TestBrowserUse_IsInstalled(t *testing.T) {
	// A path that does not exist -> not installed.
	if newBrowserUseAt(filepath.Join(t.TempDir(), "nope")).IsInstalled() {
		t.Error("expected not installed for missing binary")
	}
	// A real file -> installed.
	f := filepath.Join(t.TempDir(), "browser-use")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !newBrowserUseAt(f).IsInstalled() {
		t.Error("expected installed for existing binary")
	}
}

func TestLookupAndAll(t *testing.T) {
	if _, ok := Lookup("browser-use"); !ok {
		t.Error("browser-use should be registered")
	}
	if _, ok := Lookup("nonexistent"); ok {
		t.Error("unknown wirer should not be found")
	}
	if len(All()) == 0 {
		t.Error("All() should return at least one wirer")
	}
}
