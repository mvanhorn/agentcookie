package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSourceMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSource(dir)
	if err == nil {
		t.Fatal("expected error for missing source.yaml, got nil")
	}
	if !strings.Contains(err.Error(), "source.yaml") {
		t.Errorf("error should name the missing file, got: %v", err)
	}
}

func TestLoadSourceHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
security:
  shared_secret: not-empty
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if cfg.Sink.URL != "http://example.test:9999/sync" {
		t.Errorf("sink URL wrong: %q", cfg.Sink.URL)
	}
	if strings.HasPrefix(cfg.Chrome.DBPath, "~") {
		t.Errorf("DBPath should have tilde expanded, got %q", cfg.Chrome.DBPath)
	}
}

func TestLoadSourceMissingSinkURL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
security:
  shared_secret: not-empty
`)
	if _, err := LoadSource(dir); err == nil {
		t.Fatal("expected error for missing sink.url, got nil")
	}
}

func TestLoadSourceMissingSecret(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
`)
	if _, err := LoadSource(dir); err == nil {
		t.Fatal("expected error for missing shared_secret, got nil")
	}
}

func TestLoadSinkDefaultsListenAddr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sink.yaml", `
security:
  shared_secret: not-empty
`)
	cfg, err := LoadSink(dir)
	if err != nil {
		t.Fatalf("LoadSink: %v", err)
	}
	if cfg.Listen.Addr != "127.0.0.1:9999" {
		t.Errorf("expected default listen 127.0.0.1:9999, got %q", cfg.Listen.Addr)
	}
}

func TestLoadBlocklistHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 1
domains:
  - pattern: "%chase.com"
    description: Chase bank
  - pattern: "%1password.com"
`)
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(bl.Domains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(bl.Domains))
	}
	if bl.Domains[0].Pattern != "%chase.com" {
		t.Errorf("first pattern wrong: %q", bl.Domains[0].Pattern)
	}
}

func TestLoadBlocklistMissingReturnsEmpty(t *testing.T) {
	// v0.3: missing blocklist is NOT an error. Empty = sync-all default.
	dir := t.TempDir()
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist on missing file should not error: %v", err)
	}
	if bl == nil || len(bl.Domains) != 0 {
		t.Errorf("expected empty blocklist, got %+v", bl)
	}
}

func TestLoadBlocklistRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 99
domains: []
`)
	if _, err := LoadBlocklist(dir); err == nil {
		t.Fatal("expected error for unknown version, got nil")
	}
}

func TestLoadBlocklistRejectsEmptyPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 1
domains:
  - pattern: ""
    description: oops
`)
	if _, err := LoadBlocklist(dir); err == nil {
		t.Fatal("expected error for empty pattern, got nil")
	}
}

func TestLoadBlocklistMigratesLegacyAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "allowlist.yaml", `
version: 1
domains:
  - pattern: "%instacart.com"
`)
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(bl.Domains) != 0 {
		t.Errorf("legacy allowlist should NOT carry over to blocklist; got %d domains", len(bl.Domains))
	}
	// Legacy file should be renamed to .v2.bak.
	if _, err := os.Stat(filepath.Join(dir, "allowlist.yaml.v2.bak")); err != nil {
		t.Errorf("legacy allowlist should be renamed to .v2.bak, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "allowlist.yaml")); !os.IsNotExist(err) {
		t.Errorf("legacy allowlist should be gone after migration, got %v", err)
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := map[string]string{
		"~/foo":  filepath.Join(home, "foo"),
		"/abs":   "/abs",
		"":       "",
		"rel":    "rel",
		"~other": "~other", // bare ~ with no slash is not expanded
	}
	for in, want := range cases {
		got := ExpandTilde(in)
		if got != want {
			t.Errorf("ExpandTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
