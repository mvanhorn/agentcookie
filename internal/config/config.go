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
// Chrome profile to read from, and how transport is authenticated. After
// pairing (U5), Peer.Hostname references a key in the keystore. The
// legacy Security.SharedSecret field is kept for backwards compat with v0
// configs that predate pairing.
type SourceConfig struct {
	Sink     SinkRef     `yaml:"sink" json:"sink"`
	Chrome   ChromeRef   `yaml:"chrome" json:"chrome"`
	Peer     PeerRef     `yaml:"peer,omitempty" json:"peer,omitempty"`
	Security SecurityRef `yaml:"security,omitempty" json:"security,omitempty"`
}

// SinkConfig captures the sink machine's settings.
type SinkConfig struct {
	Listen   ListenRef   `yaml:"listen" json:"listen"`
	Chrome   ChromeRef   `yaml:"chrome" json:"chrome"`
	CDP      CDPRef      `yaml:"cdp,omitempty" json:"cdp,omitempty"`
	Peer     PeerRef     `yaml:"peer,omitempty" json:"peer,omitempty"`
	Security SecurityRef `yaml:"security,omitempty" json:"security,omitempty"`
}

// CDPRef configures live-Chrome injection on the sink via Chrome DevTools
// Protocol. When Enabled and the configured port is reachable, the sink
// writes cookies through Storage.setCookies for instant in-memory visibility
// instead of (or in addition to) the SQLite write path.
//
// When Managed is true, the sink launches and supervises its own Chrome
// subprocess with an isolated user-data-dir; Host and Port are auto-discovered
// from Chrome's DevToolsActivePort file. This is the "magical install" path
// because it avoids the macOS Keychain prompt entirely (no SQLite writes,
// no security find-generic-password call).
//
// When ExtensionDir is set, the managed Chrome is launched with
// --load-extension=<dir>. The agentcookie extension uses chrome.cookies.set()
// (a reliable API path) instead of CDP Storage.setCookies (which silently
// drops cookies). This is the v0.4 cookie-write path.
type CDPRef struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	Managed        bool   `yaml:"managed,omitempty" json:"managed,omitempty"`
	Host           string `yaml:"host,omitempty" json:"host,omitempty"`
	Port           int    `yaml:"port,omitempty" json:"port,omitempty"`
	ProfileDir     string `yaml:"profile_dir,omitempty" json:"profile_dir,omitempty"`
	ChromeBinary   string `yaml:"chrome_binary,omitempty" json:"chrome_binary,omitempty"`
	ExtensionDir   string `yaml:"extension_dir,omitempty" json:"extension_dir,omitempty"`
	ExtensionToken string `yaml:"extension_token,omitempty" json:"-"`
}

// PeerRef names the other side of a paired sync relationship. Hostname is
// the key under ~/.config/agentcookie/keys/.
type PeerRef struct {
	Hostname string `yaml:"hostname" json:"hostname"`
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
	if cfg.Peer.Hostname == "" && cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: either peer.hostname (paired key) or security.shared_secret (legacy) is required", path)
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
	if cfg.Peer.Hostname == "" && cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: either peer.hostname (paired key) or security.shared_secret (legacy) is required", path)
	}
	if cfg.Chrome.DBPath == "" {
		cfg.Chrome.DBPath = DefaultChromeCookiesPath()
	}
	if cfg.CDP.Enabled {
		if cfg.CDP.Host == "" {
			cfg.CDP.Host = "127.0.0.1"
		}
		if cfg.CDP.Port == 0 && !cfg.CDP.Managed {
			cfg.CDP.Port = 9222
		}
		if cfg.CDP.Managed && cfg.CDP.ProfileDir == "" {
			home, _ := os.UserHomeDir()
			cfg.CDP.ProfileDir = filepath.Join(home, ".agentcookie", "chrome-profile")
		}
		cfg.CDP.ProfileDir = ExpandTilde(cfg.CDP.ProfileDir)
		if cfg.CDP.Managed && cfg.CDP.ExtensionDir == "" {
			home, _ := os.UserHomeDir()
			cfg.CDP.ExtensionDir = filepath.Join(home, ".agentcookie", "extension")
		}
		cfg.CDP.ExtensionDir = ExpandTilde(cfg.CDP.ExtensionDir)
		if cfg.CDP.ExtensionToken == "" {
			cfg.CDP.ExtensionToken = "agentcookie-default-token"
		}
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
