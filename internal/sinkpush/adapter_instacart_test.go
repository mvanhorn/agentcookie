package sinkpush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// stubInstacartBinary writes a bash script at path that captures its
// stdin to capturePath and exits 0. Used to test Push without a real
// instacart-pp-cli on the box.
func stubInstacartBinary(t *testing.T, path, capturePath string) {
	t.Helper()
	script := "#!/bin/bash\ncat > " + capturePath + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}

// stubInstacartBinaryFail writes a script that always exits 1, simulating
// an `auth paste` reject.
func stubInstacartBinaryFail(t *testing.T, path string) {
	t.Helper()
	script := "#!/bin/bash\necho 'simulated paste failure' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
}

func TestInstacartAdapter_Name(t *testing.T) {
	a := NewInstacart()
	if a.Name() != "instacart-pp-cli" {
		t.Errorf("Name() = %q, want instacart-pp-cli", a.Name())
	}
}

func TestInstacartAdapter_CookieHostPatterns(t *testing.T) {
	a := NewInstacart()
	patterns := a.CookieHostPatterns()
	want := []string{"instacart.com", "%.instacart.com"}
	if len(patterns) != len(want) || patterns[0] != want[0] || patterns[1] != want[1] {
		t.Errorf("patterns: got %v, want %v", patterns, want)
	}
}

func TestInstacartAdapter_IsInstalled_True(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instacart-pp-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &InstacartAdapter{binary: bin}
	if !a.IsInstalled() {
		t.Errorf("IsInstalled() = false, want true when binary exists at %s", bin)
	}
}

func TestInstacartAdapter_IsInstalled_False(t *testing.T) {
	a := &InstacartAdapter{binary: "/nonexistent/path/instacart-pp-cli"}
	if a.IsInstalled() {
		t.Errorf("IsInstalled() = true, want false when binary missing")
	}
}

func TestInstacartAdapter_IsInstalled_IsDir(t *testing.T) {
	dir := t.TempDir()
	a := &InstacartAdapter{binary: dir}
	if a.IsInstalled() {
		t.Errorf("IsInstalled() = true on directory path, want false")
	}
}

func TestInstacartAdapter_Push_FormatsAndExecs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instacart-pp-cli")
	capturePath := filepath.Join(dir, "stdin.txt")
	stubInstacartBinary(t, bin, capturePath)

	a := &InstacartAdapter{binary: bin}
	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "tok123", Path: "/"},
		{HostKey: ".instacart.com", Name: "csrf", Value: "abc", Path: "/"},
		{HostKey: "www.instacart.com", Name: "device_uuid", Value: "deadbeef", Path: "/"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	got := string(captured)
	for _, want := range []string{"_session=tok123", "csrf=abc", "device_uuid=deadbeef"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdin to auth paste missing %q. Full input: %q", want, got)
		}
	}
	// Verify the header separator is "; " (with space) per Cookie spec.
	if !strings.Contains(got, "; ") {
		t.Errorf("expected Cookie header separator '; ', got: %q", got)
	}
}

func TestInstacartAdapter_Push_FiltersEmptyValueCookies(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instacart-pp-cli")
	capturePath := filepath.Join(dir, "stdin.txt")
	stubInstacartBinary(t, bin, capturePath)

	a := &InstacartAdapter{binary: bin}
	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "real-token", Path: "/"},
		{HostKey: ".instacart.com", Name: "consent_flag", Value: "", Path: "/"}, // empty -> drop
		{HostKey: ".instacart.com", Name: "csrf", Value: "abc", Path: "/"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, _ := os.ReadFile(capturePath)
	if strings.Contains(string(got), "consent_flag") {
		t.Errorf("empty-value cookie should have been dropped from Cookie header, got: %q", string(got))
	}
	if !strings.Contains(string(got), "_session=real-token") {
		t.Errorf("real cookie missing from header: %q", string(got))
	}
}

func TestInstacartAdapter_Push_AllEmptyValues_NoExec(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instacart-pp-cli")
	capturePath := filepath.Join(dir, "stdin.txt")
	stubInstacartBinary(t, bin, capturePath)

	a := &InstacartAdapter{binary: bin}
	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "consent", Value: ""},
		{HostKey: ".instacart.com", Name: "other", Value: ""},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Stub should not have run -- if it had, capturePath would exist.
	if _, err := os.Stat(capturePath); err == nil {
		t.Errorf("expected CLI not invoked when all cookies have empty Value, but capture file exists")
	}
}

func TestInstacartAdapter_Push_PropagatesCLIError(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "instacart-pp-cli")
	stubInstacartBinaryFail(t, bin)

	a := &InstacartAdapter{binary: bin}
	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "tok"},
	}
	err := a.Push(cookies)
	if err == nil {
		t.Fatal("expected error from failing CLI, got nil")
	}
	if !strings.Contains(err.Error(), "auth paste") {
		t.Errorf("error should mention 'auth paste', got: %v", err)
	}
	if !strings.Contains(err.Error(), "simulated paste failure") {
		t.Errorf("error should include stderr from CLI ('simulated paste failure'), got: %v", err)
	}
}

func TestFormatCookieHeader_OrderPreserved(t *testing.T) {
	// Adapter receives cookies pre-filtered in source order; the
	// header should preserve that order so debugging diffs are
	// stable across runs.
	cookies := []chrome.Cookie{
		{Name: "a", Value: "1"},
		{Name: "b", Value: "2"},
		{Name: "c", Value: "3"},
	}
	got := formatCookieHeader(cookies)
	want := "a=1; b=2; c=3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatCookieHeader_Empty(t *testing.T) {
	if got := formatCookieHeader(nil); got != "" {
		t.Errorf("nil input: got %q, want empty", got)
	}
	if got := formatCookieHeader([]chrome.Cookie{}); got != "" {
		t.Errorf("empty slice: got %q, want empty", got)
	}
}

func TestInstacartAdapter_CachedNonExecutable_ReResolvesToValid(t *testing.T) {
	// If the adapter is initialized with a cached path that points to a
	// non-executable file, resolveBinary() should re-resolve via findPPCLI
	// and find a valid executable elsewhere. This is the fix for "cached
	// fallback blocks re-resolution" where a junk file at the preferred
	// location could cause IsInstalled() to succeed but Push() to fail.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("PATH", "")

	localBin := filepath.Join(tmpHome, ".local", "bin")
	goBin := filepath.Join(tmpHome, "go", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatal(err)
	}

	// Put a non-executable junk file at .local/bin (mode 0644).
	junkPath := filepath.Join(localBin, "instacart-pp-cli")
	if err := os.WriteFile(junkPath, []byte("junk, not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Put a valid executable at go/bin.
	validPath := filepath.Join(goBin, "instacart-pp-cli")
	if err := os.WriteFile(validPath, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create adapter with the non-executable path cached (simulates init() before install).
	a := &InstacartAdapter{binary: junkPath}

	// resolveBinary() should re-resolve since the cached path is not executable.
	resolved := a.resolveBinary()
	if resolved != validPath {
		t.Errorf("resolveBinary() with non-executable cached: got %q, want %q", resolved, validPath)
	}

	// IsInstalled() should report true (finds the valid executable).
	if !a.IsInstalled() {
		t.Errorf("IsInstalled() = false, want true when valid executable exists at %q", validPath)
	}
}

func TestInstacartAdapter_CachedExecutable_UsesCache(t *testing.T) {
	// If the cached binary is executable, resolveBinary() should use it
	// without re-resolving. This ensures the direct test injection pattern
	// still works.
	dir := t.TempDir()
	bin := filepath.Join(dir, "custom-location")
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &InstacartAdapter{binary: bin}

	// Should use the cached path since it's executable.
	resolved := a.resolveBinary()
	if resolved != bin {
		t.Errorf("resolveBinary() with executable cached: got %q, want %q", resolved, bin)
	}

	if !a.IsInstalled() {
		t.Errorf("IsInstalled() = false, want true for cached executable")
	}
}
