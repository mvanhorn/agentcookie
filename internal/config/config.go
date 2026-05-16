// Package config loads agentcookie's on-disk configuration: source.yaml,
// sink.yaml, and allowlist.yaml. Each file is independently optional so
// `agentcookie status` can report partial state.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SourceConfig captures the source machine's settings: where to push, which
// Chrome profile to read from, and the shared transport secret (pre-U5).
type SourceConfig struct {
	Sink     SinkRef     `yaml:"sink" json:"sink"`
	Chrome   ChromeRef   `yaml:"chrome" json:"chrome"`
	Security SecurityRef `yaml:"security" json:"security"`
}

// SinkConfig captures the sink machine's settings.
type SinkConfig struct {
	Listen   ListenRef   `yaml:"listen" json:"listen"`
	Chrome   ChromeRef   `yaml:"chrome" json:"chrome"`
	Security SecurityRef `yaml:"security" json:"security"`
}

type SinkRef struct {
	URL string `yaml:"url" json:"url"`
}

type ListenRef struct {
	Addr string `yaml:"addr" json:"addr"`
}

type ChromeRef struct {
	DBPath string `yaml:"db_path" json:"db_path"`
}

// SecurityRef holds transport credentials. SharedSecret is the pre-pairing
// stopgap; U5 replaces it with a pairing-derived per-peer key persisted in the
// OS keychain.
type SecurityRef struct {
	SharedSecret string `yaml:"shared_secret" json:"-"` // never marshal to JSON
}

// LoadSource reads source.yaml from dir.
func LoadSource(dir string) (*SourceConfig, error) {
	path := filepath.Join(dir, "source.yaml")
	var cfg SourceConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	cfg.Chrome.DBPath = ExpandTilde(cfg.Chrome.DBPath)
	if cfg.Sink.URL == "" {
		return nil, fmt.Errorf("%s: sink.url is required", path)
	}
	if cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: security.shared_secret is required (will be replaced by pairing in U5)", path)
	}
	if cfg.Chrome.DBPath == "" {
		cfg.Chrome.DBPath = DefaultChromeCookiesPath()
	}
	return &cfg, nil
}

// LoadSink reads sink.yaml from dir.
func LoadSink(dir string) (*SinkConfig, error) {
	path := filepath.Join(dir, "sink.yaml")
	var cfg SinkConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	cfg.Chrome.DBPath = ExpandTilde(cfg.Chrome.DBPath)
	if cfg.Listen.Addr == "" {
		cfg.Listen.Addr = "127.0.0.1:9999"
	}
	if cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: security.shared_secret is required (will be replaced by pairing in U5)", path)
	}
	if cfg.Chrome.DBPath == "" {
		cfg.Chrome.DBPath = DefaultChromeCookiesPath()
	}
	return &cfg, nil
}

func loadYAML(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("not found: %s (start from examples/ in this repo)", path)
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// ExpandTilde turns a leading "~/" into the user's home dir. Leaves all other
// paths alone.
func ExpandTilde(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}

// DefaultChromeCookiesPath returns the default Chrome cookies SQLite path on
// macOS. Kept here so config can populate omitted db_path fields without
// importing chrome (which pulls CGO sqlite).
func DefaultChromeCookiesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "Cookies")
}
