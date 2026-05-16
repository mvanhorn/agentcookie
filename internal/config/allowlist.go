package config

import (
	"fmt"
	"path/filepath"
)

// AllowlistEntry is one explicitly opted-in cookie domain. Pattern follows
// SQLite LIKE syntax (use '%' as wildcard, e.g. "%instacart.com").
type AllowlistEntry struct {
	Pattern     string `yaml:"pattern" json:"pattern"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Allowlist is the on-disk shape of allowlist.yaml. Version is bumped if/when
// the file format changes incompatibly.
type Allowlist struct {
	Version int              `yaml:"version" json:"version"`
	Domains []AllowlistEntry `yaml:"domains" json:"domains"`
}

// LoadAllowlist reads allowlist.yaml from dir.
func LoadAllowlist(dir string) (*Allowlist, error) {
	path := filepath.Join(dir, "allowlist.yaml")
	var al Allowlist
	if err := loadYAML(path, &al); err != nil {
		return nil, err
	}
	if al.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported allowlist version %d (this binary speaks version 1)", path, al.Version)
	}
	for i, e := range al.Domains {
		if e.Pattern == "" {
			return nil, fmt.Errorf("%s: domains[%d].pattern is empty", path, i)
		}
	}
	return &al, nil
}
