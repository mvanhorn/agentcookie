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

func TestLoadAllowlistHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "allowlist.yaml", `
version: 1
domains:
  - pattern: "%instacart.com"
    description: Instacart
  - pattern: "%example.com"
`)
	al, err := LoadAllowlist(dir)
	if err != nil {
		t.Fatalf("LoadAllowlist: %v", err)
	}
	if len(al.Domains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(al.Domains))
	}
	if al.Domains[0].Pattern != "%instacart.com" {
		t.Errorf("first pattern wrong: %q", al.Domains[0].Pattern)
	}
}

func TestLoadAllowlistRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "allowlist.yaml", `
version: 99
domains: []
`)
	if _, err := LoadAllowlist(dir); err == nil {
		t.Fatal("expected error for unknown version, got nil")
	}
}

func TestLoadAllowlistRejectsEmptyPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "allowlist.yaml", `
version: 1
domains:
  - pattern: ""
    description: oops
`)
	if _, err := LoadAllowlist(dir); err == nil {
		t.Fatal("expected error for empty pattern, got nil")
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
