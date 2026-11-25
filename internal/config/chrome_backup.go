package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ChromeBackupFile holds the owner's chrome-backup settings, next to
// source.yaml.
const ChromeBackupFile = "chrome-backup.yaml"

// ChromeBackupConfig is the parsed chrome-backup.yaml.
type ChromeBackupConfig struct {
	// ExtraDirs are folders, relative to each Chrome profile folder, that
	// the daily full snapshot also copies, e.g.
	// "IndexedDB/https_example.com_0.indexeddb.leveldb" for one site's
	// IndexedDB. Profiles without the folder are skipped.
	ExtraDirs []string `yaml:"extra_dirs,omitempty"`
}

// ChromeBackupPath returns the chrome-backup.yaml path under dir.
func ChromeBackupPath(dir string) string { return filepath.Join(dir, ChromeBackupFile) }

// LoadChromeBackup reads chrome-backup.yaml from dir. A missing or empty
// file gives an empty config. Each extra dir must be a plain relative path
// inside the profile folder; duplicates are an error.
func LoadChromeBackup(dir string) (*ChromeBackupConfig, error) {
	p := ChromeBackupPath(dir)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ChromeBackupConfig{}, nil
		}
		return nil, fmt.Errorf("stat %s: %w", p, err)
	}
	var cfg ChromeBackupConfig
	if err := loadYAML(p, &cfg); err != nil {
		if !isEmptyYAML(p) {
			return nil, err
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(cfg.ExtraDirs))
	for i, d := range cfg.ExtraDirs {
		clean, err := cleanProfileRelDir(d)
		if err != nil {
			return nil, fmt.Errorf("%s: extra_dirs[%d] %q: %w", p, i, d, err)
		}
		if seen[clean] {
			return nil, fmt.Errorf("%s: extra_dirs[%d] %q is listed more than once", p, i, d)
		}
		seen[clean] = true
		out = append(out, clean)
	}
	cfg.ExtraDirs = out
	return &cfg, nil
}

// cleanProfileRelDir checks that d names a folder inside a profile folder
// and returns it cleaned, with "/" separators and no trailing slash.
func cleanProfileRelDir(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return "", errors.New("is empty")
	}
	if strings.ContainsRune(d, '\\') {
		return "", errors.New("use / as the separator")
	}
	if strings.HasPrefix(d, "/") || strings.HasPrefix(d, "~") {
		return "", errors.New("must be relative to the profile folder")
	}
	clean := path.Clean(d)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("must stay inside the profile folder")
	}
	return clean, nil
}
