package agentattach

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDebugProfileDir(t *testing.T) {
	d := DefaultDebugProfileDir()
	if !strings.HasSuffix(d, filepath.Join(".agentcookie", "chrome-debug")) {
		t.Errorf("DefaultDebugProfileDir = %q, want .../.agentcookie/chrome-debug", d)
	}
}

func TestLaunchArgs_LoopbackAndCustomDir(t *testing.T) {
	dp := &DebugProfile{Dir: "/tmp/acdbg", Port: 9333}
	args := dp.LaunchArgs()
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--remote-debugging-port=9333",
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=/tmp/acdbg",
		"--no-first-run",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("LaunchArgs missing %q in %q", want, joined)
		}
	}
	// Must never point at the real default profile.
	if strings.Contains(joined, "Application Support/Google/Chrome/Default") {
		t.Errorf("LaunchArgs must not target the default profile: %q", joined)
	}
}

func TestSeedCookies_EmptyIsNoop(t *testing.T) {
	dp := &DebugProfile{Dir: t.TempDir(), Port: 9333}
	// Empty cookie set must not spawn Chrome or error.
	if err := dp.SeedCookies(context.Background(), nil); err != nil {
		t.Errorf("SeedCookies(nil) = %v, want nil", err)
	}
}

func TestCopyLocalStorage_MissingSourceIsZero(t *testing.T) {
	dp := &DebugProfile{Dir: t.TempDir(), Port: 9333}
	n, err := dp.CopyLocalStorage(t.TempDir()) // no "Local Storage" subdir
	if err != nil {
		t.Fatalf("CopyLocalStorage: %v", err)
	}
	if n != 0 {
		t.Errorf("missing source should copy 0 files, got %d", n)
	}
}

func TestCopyLocalStorage_CopiesTree(t *testing.T) {
	srcProfile := t.TempDir()
	// Build a fake Local Storage/leveldb tree.
	ls := filepath.Join(srcProfile, "Local Storage", "leveldb")
	if err := os.MkdirAll(ls, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ls, "000005.ldb"), "ldb-bytes")
	mustWrite(t, filepath.Join(ls, "MANIFEST-000001"), "manifest")

	dp := &DebugProfile{Dir: t.TempDir(), Port: 9333}
	n, err := dp.CopyLocalStorage(srcProfile)
	if err != nil {
		t.Fatalf("CopyLocalStorage: %v", err)
	}
	if n != 2 {
		t.Errorf("copied %d files, want 2", n)
	}
	got := readFile(t, filepath.Join(dp.Dir, "Local Storage", "leveldb", "000005.ldb"))
	if got != "ldb-bytes" {
		t.Errorf("copied content = %q, want ldb-bytes", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Launch, waitReachable, and Stop require a real Chrome binary on a
// loopback debug port and are exercised by manual/integration runs, not
// unit tests; the pure seed/copy/args helpers above carry the unit coverage.

func TestCopyTree_SkipsSymlinks(t *testing.T) {
	srcProfile := t.TempDir()
	ls := filepath.Join(srcProfile, "Local Storage", "leveldb")
	if err := os.MkdirAll(ls, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ls, "000005.ldb"), "real")
	// A symlink pointing outside the source tree must not be copied.
	outside := filepath.Join(t.TempDir(), "secret")
	mustWrite(t, outside, "do-not-copy")
	if err := os.Symlink(outside, filepath.Join(ls, "evil-link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	dp := &DebugProfile{Dir: t.TempDir(), Port: 9333}
	n, err := dp.CopyLocalStorage(srcProfile)
	if err != nil {
		t.Fatalf("CopyLocalStorage: %v", err)
	}
	if n != 1 {
		t.Errorf("copied %d files, want 1 (symlink skipped)", n)
	}
	if _, err := os.Lstat(filepath.Join(dp.Dir, "Local Storage", "leveldb", "evil-link")); err == nil {
		t.Error("symlink should not have been copied into the debug profile")
	}
}
