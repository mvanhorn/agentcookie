package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFreshBlocklistMissingReturnsEmpty(t *testing.T) {
	withConfigDir(t, t.TempDir())

	bl, err := loadFreshBlocklist()
	if err != nil {
		t.Fatalf("loadFreshBlocklist missing file: %v", err)
	}
	if bl == nil {
		t.Fatal("loadFreshBlocklist returned nil blocklist for missing file")
	}
	if bl.Version != 1 {
		t.Errorf("Version = %d, want 1", bl.Version)
	}
	if len(bl.Domains) != 0 {
		t.Errorf("missing blocklist should be sync-all empty, got %d domains", len(bl.Domains))
	}
}

func TestLoadFreshBlocklistWellFormed(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "chase.com"
  - pattern: "%.chase.com"
`)

	bl, err := loadFreshBlocklist()
	if err != nil {
		t.Fatalf("loadFreshBlocklist valid file: %v", err)
	}
	if bl == nil {
		t.Fatal("loadFreshBlocklist returned nil blocklist for valid file")
	}
	if len(bl.Domains) != 2 {
		t.Fatalf("len(Domains) = %d, want 2", len(bl.Domains))
	}
	if bl.Domains[0].Pattern != "chase.com" || bl.Domains[1].Pattern != "%.chase.com" {
		t.Errorf("patterns = %#v", bl.Domains)
	}
}

func TestLoadFreshBlocklistRejectsUnknownTopLevelKey(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
domains: []
unexpected: true
`)

	bl, err := loadFreshBlocklist()
	if err == nil {
		t.Fatalf("loadFreshBlocklist should reject unknown top-level key, got blocklist %+v", bl)
	}
	if !strings.Contains(err.Error(), "field unexpected not found") {
		t.Errorf("error should mention unknown key, got %v", err)
	}
}

func TestLoadFreshBlocklistRejectsTruncatedFile(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.chas
`)

	bl, err := loadFreshBlocklist()
	if err == nil {
		t.Fatalf("loadFreshBlocklist should reject truncated YAML, got blocklist %+v", bl)
	}
}

func TestLoadFreshBlocklistRejectsUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 2
domains: []
`)

	bl, err := loadFreshBlocklist()
	if err == nil {
		t.Fatalf("loadFreshBlocklist should reject unsupported version, got blocklist %+v", bl)
	}
	if !strings.Contains(err.Error(), "unsupported blocklist version 2") {
		t.Errorf("error should mention unsupported version, got %v", err)
	}
}

func withConfigDir(t *testing.T, dir string) {
	t.Helper()
	oldDir := common.ConfigDir
	common.ConfigDir = dir
	t.Cleanup(func() { common.ConfigDir = oldDir })
}

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
