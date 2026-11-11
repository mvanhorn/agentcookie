package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// SourceOverrides are explicit command-line selectors. Empty fields retain
// their configuration values. Resolution never mutates the supplied config.
type SourceOverrides struct{ Browser, Profile, Reader string }

// EffectiveSource merges selectors before computing paths. A previously
// computed default is discarded when a selector changes; explicit db_path is
// retained. CDP is exclusive and never falls back to filesystem readers.
func EffectiveSource(cfg *SourceConfig, overrides SourceOverrides) (*SourceConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("source configuration is required")
	}
	out := *cfg
	if out.CDPSource.Enabled && (overrides.Browser != "" || overrides.Profile != "" || overrides.Reader != "") {
		return nil, fmt.Errorf("cdp_source cannot be combined with browser overrides")
	}
	if (overrides.Browser != "" || overrides.Profile != "") && out.resolvedDBPath != "" && out.Chrome.DBPath == out.resolvedDBPath {
		out.Chrome.DBPath = ""
		out.resolvedDBPath = ""
	}
	if overrides.Browser != "" {
		out.Browser.Name = overrides.Browser
	}
	if overrides.Profile != "" {
		out.Browser.Profile = overrides.Profile
	}
	if overrides.Reader != "" {
		out.Browser.Reader = overrides.Reader
	}
	if err := resolveSourcePaths("source", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LoadSourceLocalWithOverrides ensures selectors are applied before path
// resolution, including when the configured browser is no longer available.
func LoadSourceLocalWithOverrides(dir string, overrides SourceOverrides) (*SourceConfig, error) {
	path := filepath.Join(dir, "source.yaml")
	var cfg SourceConfig
	if _, err := os.Stat(path); err == nil {
		if err := loadYAML(path, &cfg); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return EffectiveSource(&cfg, overrides)
}
