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
//
// SkipChromeSQLite is the v0.12.0-beta.3 headless-sink flag. When true,
// the sink never reads Chrome Safe Storage and never writes Chrome's
// SQLite/leveldb/indexeddb files. The sidecar (~/.agentcookie/cookies-plain.db,
// pair-derived shared key) and adapter push (per-PP-CLI session files)
// remain the cookie-delivery paths and are unaffected. This unblocks
// SSH-only installs on headless Mac minis where no GUI session can
// answer the Chrome Safe Storage Keychain prompt.
//
// Delivery is the v0.13 universal-cookie-delivery marker. It records the
// INTENT a wizard install resolved to, so `doctor` can report "any cookie
// CLI works here" vs "degraded" without re-inferring it from
// SkipChromeSQLite + keychain probe state. Values: "universal" (real
// Default Chrome profile + any-app keychain open) or "degraded" (the
// -T/skip_chrome_sqlite opt-out). It is omitempty: an existing sink.yaml
// written before this field keeps its current behavior with no migration
// and no silent flip on a binary upgrade.
type SinkConfig struct {
	Listen           ListenRef   `yaml:"listen" json:"listen"`
	Chrome           ChromeRef   `yaml:"chrome" json:"chrome"`
	Peer             PeerRef     `yaml:"peer,omitempty" json:"peer,omitempty"`
	Security         SecurityRef `yaml:"security,omitempty" json:"security,omitempty"`
	SkipChromeSQLite bool        `yaml:"skip_chrome_sqlite,omitempty" json:"skip_chrome_sqlite,omitempty"`
	CDP              CDPRef      `yaml:"cdp,omitempty" json:"cdp,omitempty"`
	Delivery         string      `yaml:"delivery,omitempty" json:"delivery,omitempty"`
}

// CDPRef configures the v0.12.0-beta.3 CDP-injection mode. When Enabled,
// the sink launches a headless Chrome via chromedp after each /sync and
// pushes the synced cookies through Storage.setCookies. Chrome encrypts
// its own SQLite with its own Safe Storage key; agentcookie never reads
// Chrome's Keychain item on this path.
type CDPRef struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	ProfileDir string `yaml:"profile_dir,omitempty" json:"profile_dir,omitempty"`
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
	if err := validateSharedSecret(path, cfg.Security.SharedSecret); err != nil {
		return nil, err
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
	// v0.12 S1: no permissive default for listen.addr. Pre-v0.12 sink.yaml
	// files that omit the address used to fall through to 127.0.0.1:9999;
	// that masked the wizard's silent-detection-failure -> 0.0.0.0 path
	// further upstream. Empty here is now a config error; the wizard
	// install writes a concrete tailnet 100.x address (see
	// internal/tsclient.RequireTailnetIP).
	if cfg.Listen.Addr == "" {
		return nil, fmt.Errorf("%s: listen.addr is required (run `agentcookie wizard install --as sink` to detect your Tailscale 100.x address, or set it explicitly)", path)
	}
	if cfg.Peer.Hostname == "" && cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: either peer.hostname (paired key) or security.shared_secret (legacy) is required", path)
	}
	if err := validateSharedSecret(path, cfg.Security.SharedSecret); err != nil {
		return nil, err
	}
	if cfg.Chrome.DBPath == "" {
		cfg.Chrome.DBPath = DefaultChromeCookiesPath()
	}
	return &cfg, nil
}

// validateSharedSecret enforces a 32-byte minimum on the legacy
// security.shared_secret YAML field. v0.12 rejects shorter values
// because newGCM derives the AES key by SHA-256-hashing the secret;
// a short secret produced a weak AEAD key. Pairing-derived 32-byte
// keys bypass the SHA-256 step entirely and never fail this check.
// Empty secret is permitted here: the paired-key path is the
// canonical credential and the legacy field is optional.
func validateSharedSecret(path, secret string) error {
	if secret == "" {
		return nil
	}
	if len(secret) < 32 {
		return fmt.Errorf("%s: security.shared_secret must be at least 32 bytes (got %d); prefer pairing (`agentcookie pair`) over a typed secret", path, len(secret))
	}
	return nil
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
