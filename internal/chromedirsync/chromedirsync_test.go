package chromedirsync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPack_HappyPath(t *testing.T) {
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "origin1.example.com_0", "MANIFEST-000001"), []byte("manifest-bytes"))
	mustWriteFile(t, filepath.Join(src, "origin1.example.com_0", "000003.log"), []byte("log-bytes-here"))
	mustWriteFile(t, filepath.Join(src, "origin2.example.com_0", "MANIFEST-000001"), []byte("two"))

	payload, skipped, err := Pack(src, 0)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("expected non-empty payload")
	}
	if len(skipped) != 0 {
		t.Errorf("expected no origins skipped, got %v", skipped)
	}

	dst := t.TempDir()
	stagingDir := filepath.Join(dst, "Local Storage.staging")
	if err := Unpack(payload, stagingDir); err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(stagingDir, "origin1.example.com_0", "000003.log")); string(got) != "log-bytes-here" {
		t.Errorf("file contents wrong after unpack: %q", got)
	}
	if got := mustReadFile(t, filepath.Join(stagingDir, "origin2.example.com_0", "MANIFEST-000001")); string(got) != "two" {
		t.Errorf("file contents wrong: %q", got)
	}
}

func TestPack_ErrSourceMissing(t *testing.T) {
	_, _, err := Pack("/tmp/this-dir-does-not-exist-agentcookie-test", 0)
	if !errors.Is(err, ErrSourceMissing) {
		t.Errorf("expected ErrSourceMissing, got %v", err)
	}
}

func TestPack_OriginSizeFilter(t *testing.T) {
	src := t.TempDir()
	// small origin (50 bytes)
	mustWriteFile(t, filepath.Join(src, "small.example.com_0", "log"), make([]byte, 50))
	// large origin (200 bytes, over our 100 cap)
	mustWriteFile(t, filepath.Join(src, "big.example.com_0", "log"), make([]byte, 200))

	payload, skipped, err := Pack(src, 100)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != "big.example.com_0" {
		t.Errorf("expected only big.example.com_0 skipped, got %v", skipped)
	}

	dst := t.TempDir()
	if err := Unpack(payload, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "small.example.com_0", "log")); err != nil {
		t.Errorf("small origin should be present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "big.example.com_0")); err == nil {
		t.Errorf("big origin should be excluded")
	}
}

func TestAtomicReplaceDir(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "leveldb")
	staging := filepath.Join(base, "leveldb.staging")
	mustWriteFile(t, filepath.Join(live, "old"), []byte("old"))
	mustWriteFile(t, filepath.Join(staging, "new"), []byte("new"))

	if err := AtomicReplaceDir(staging, live); err != nil {
		t.Fatalf("AtomicReplaceDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(live, "new")); err != nil {
		t.Errorf("new file should be in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(live, "old")); err == nil {
		t.Errorf("old file should be gone")
	}
	if _, err := os.Stat(staging); err == nil {
		t.Errorf("staging should be consumed by rename")
	}
	if _, err := os.Stat(live + ".agentcookie.bak"); err == nil {
		t.Errorf("bak should be removed after successful rename")
	}
}

func TestAtomicReplaceDir_NoLiveDir(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "leveldb")
	staging := filepath.Join(base, "leveldb.staging")
	mustWriteFile(t, filepath.Join(staging, "new"), []byte("new"))

	if err := AtomicReplaceDir(staging, live); err != nil {
		t.Fatalf("AtomicReplaceDir: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(live, "new")); string(got) != "new" {
		t.Errorf("new file contents wrong: %q", got)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
