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
// Protocol. When Enabled is set, the sink dispatches by Mode:
//
//   - Mode "attach" (v0.5 default): attach to the user's running Chrome via
//     the chrome://inspect/#remote-debugging activation. AttachProfileDir
//     overrides the discovery target (defaults to the user's default Chrome
//     profile).
//   - Mode "managed" (legacy): spawn an isolated Chrome subprocess at
//     ProfileDir and write via hand-rolled CDP. Known to drop cookies
//     silently. Retained for headless deployments only.
type CDPRef struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Mode selects the cookie-write path. Values:
	//   "attach"  - v0.5+ default; connect to the user's running Chrome via
	//               chrome://inspect/#remote-debugging activation. Sink discovers
	//               the CDP endpoint from DevToolsActivePort in the default
	//               profile and connects via chromedp.
	//   "managed" - legacy; sink spawns its own Chrome subprocess. Retained
	//               for headless deployments where no one can flip the
	//               chrome://inspect toggle. Cookie writes go through
	//               hand-rolled CDP and are known to drop some cookies.
	//   ""        - back-compat: derive mode from the legacy Managed field.
	//               If Managed=true -> "managed"; otherwise -> "attach"
	//               (v0.5 default).
	//
	// Set in YAML as `cdp.mode: attach` or `cdp.mode: managed`.
	Mode         string `yaml:"mode,omitempty" json:"mode,omitempty"`
	Managed      bool   `yaml:"managed,omitempty" json:"managed,omitempty"`
	Host         string `yaml:"host,omitempty" json:"host,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitempty"`
	ProfileDir   string `yaml:"profile_dir,omitempty" json:"profile_dir,omitempty"`
	ChromeBinary string `yaml:"chrome_binary,omitempty" json:"chrome_binary,omitempty"`
	// AttachProfileDir overrides the default Chrome profile directory used by
	// attach mode. Default: ~/Library/Application Support/Google/Chrome on
	// macOS. Useful for tests and multi-profile setups.
	AttachProfileDir string `yaml:"attach_profile_dir,omitempty" json:"attach_profile_dir,omitempty"`
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
		if cfg.CDP.Mode == "" {
			if cfg.CDP.Managed {
				cfg.CDP.Mode = "managed"
			} else {
				cfg.CDP.Mode = "attach"
			}
		}
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
		cfg.CDP.AttachProfileDir = ExpandTilde(cfg.CDP.AttachProfileDir)
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
