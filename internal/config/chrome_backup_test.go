package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeChromeBackup(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ChromeBackupFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadChromeBackupMissingOrEmptyIsEmpty(t *testing.T) {
	for name, dir := range map[string]string{"missing": t.TempDir(), "empty": writeChromeBackup(t, "  \n")} {
		cfg, err := LoadChromeBackup(dir)
		if err != nil || cfg == nil || len(cfg.ExtraDirs) != 0 {
			t.Fatalf("%s: cfg=%+v err=%v", name, cfg, err)
		}
	}
}

func TestLoadChromeBackupExtraDirs(t *testing.T) {
	dir := writeChromeBackup(t, "extra_dirs:\n  - IndexedDB/https_play.basketball-gm.com_0.indexeddb.leveldb/\n  - ./File System\n")
	cfg, err := LoadChromeBackup(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"IndexedDB/https_play.basketball-gm.com_0.indexeddb.leveldb", "File System"}
	if !slices.Equal(cfg.ExtraDirs, want) {
		t.Fatalf("ExtraDirs = %v, want %v", cfg.ExtraDirs, want)
	}
}

func TestLoadChromeBackupRejectsBadDirs(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"extra_dirs:\n  - /Users/me/x\n", "relative"},
		{"extra_dirs:\n  - ~/x\n", "relative"},
		{"extra_dirs:\n  - ../Default/Cookies\n", "inside"},
		{"extra_dirs:\n  - IndexedDB/../..\n", "inside"},
		{"extra_dirs:\n  - .\n", "inside"},
		{"extra_dirs:\n  - \"\"\n", "empty"},
		{"extra_dirs:\n  - 'IndexedDB\\x'\n", "separator"},
		{"extra_dirs:\n  - IndexedDB/a\n  - IndexedDB/a/\n", "more than once"},
		{"extra_dir:\n  - IndexedDB/a\n", "extra_dir"},
	} {
		_, err := LoadChromeBackup(writeChromeBackup(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to mention %q", tc.body, err, tc.want)
		}
	}
}
