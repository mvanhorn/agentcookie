// Package config loads agentcookie's on-disk configuration: source.yaml,
// sink.yaml, and blocklist.yaml. Each file is independently optional so
// `agentcookie status` can report partial state.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Browser  BrowserRef  `yaml:"browser,omitempty" json:"browser,omitempty"`
	Peer     PeerRef     `yaml:"peer,omitempty" json:"peer,omitempty"`
	Security SecurityRef `yaml:"security,omitempty" json:"security,omitempty"`
	// Cmux configures the same-machine local loop: `agentcookie cmux-sync`
	// reads this machine's Chrome and injects into this machine's cmux
	// browser. Independent of the sink/peer push path; absent = loop off.
	// Reuses the CmuxRef shape (see SinkConfig.Cmux).
	Cmux CmuxRef `yaml:"cmux,omitempty" json:"cmux,omitempty"`
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
	Cmux             CmuxRef     `yaml:"cmux,omitempty" json:"cmux,omitempty"`
	Delivery         string      `yaml:"delivery,omitempty" json:"delivery,omitempty"`
}

// CmuxRef configures the cmux cookie-delivery surface (a fourth surface
// alongside Chrome SQLite, the sidecar, and the per-CLI adapters). When
// Enabled, the sink injects the synced cookies into cmux's embedded
// WebKit browser after each /sync via `cmux rpc browser.cookies.set`, so
// an agent driving cmux's browser wakes up authenticated. cmux holds its
// own WebKit cookie jar (separate from Chrome's SQLite), so this surface
// is purely additive.
//
// omitempty keeps a pre-cmux sink.yaml valid with the surface off: an
// absent block decodes to Enabled=false and the sink never touches cmux.
//
// NOTE: cmux's RPC socket defaults to socketControlMode "cmuxOnly", which
// rejects this sink (a LaunchAgent, not a cmux child). `agentcookie
// doctor` detects that and prints the one-line remediation
// (socketControlMode allowAll/password in ~/.config/cmux/cmux.json, then
// a full cmux restart -- the mode is read only at app launch).
type CmuxRef struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// CmuxPath overrides the cmux CLI location. Empty uses the
	// canonical app-bundle path with a PATH fallback (see
	// internal/sinkpush.NewCmux).
	CmuxPath string `yaml:"cmux_path,omitempty" json:"cmux_path,omitempty"`
	// DomainFilter narrows which cookies reach cmux, as SQLite-LIKE
	// host_key patterns (e.g. "%github.com"). Empty means deliver the
	// full synced set (after the sink's blocklist filter).
	DomainFilter []string `yaml:"domain_filter,omitempty" json:"domain_filter,omitempty"`
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

type BrowserRef struct {
	Name    string `yaml:"name" json:"name"`
	Profile string `yaml:"profile" json:"profile"`
}

type browserPathRef struct {
	SupportDir []string
}

const (
	defaultBrowserName    = "chrome"
	defaultBrowserProfile = "Default"
)

// Mirror of internal/chrome's browser registry (path side only). Kept in
// sync by the guard tests in internal/cli; config stays free of the chrome
// package's CGO sqlite dependency. See internal/chrome/browser.go for the
// keychain strings and per-browser notes.
var sourceBrowserPaths = map[string]browserPathRef{
	defaultBrowserName: {SupportDir: []string{"Google", "Chrome"}},
	"atlas":            {SupportDir: []string{"com.openai.atlas", "browser-data", "host"}},
	"brave":            {SupportDir: []string{"BraveSoftware", "Brave-Browser"}},
	"edge":             {SupportDir: []string{"Microsoft Edge"}},
	"arc":              {SupportDir: []string{"Arc", "User Data"}},
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
	if cfg.Sink.URL == "" {
		return nil, fmt.Errorf("%s: sink.url is required", path)
	}
	if cfg.Peer.Hostname == "" && cfg.Security.SharedSecret == "" {
		return nil, fmt.Errorf("%s: either peer.hostname (paired key) or security.shared_secret (legacy) is required", path)
	}
	if err := validateSharedSecret(path, cfg.Security.SharedSecret); err != nil {
		return nil, err
	}
	if err := resolveSourcePaths(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadSourceLocal loads source.yaml for local-only consumers like
// `cmux-sync`, which read Chrome and act on this machine alone. Unlike
// LoadSource it does NOT require sink.url or a peer/secret -- the local
// loop has no push target, so demanding push config would break the
// documented "no sink, no peer" use case. A missing source.yaml is fine:
// it yields defaults (default Chrome path, no blocklist, cmux off).
func LoadSourceLocal(dir string) (*SourceConfig, error) {
	path := filepath.Join(dir, "source.yaml")
	var cfg SourceConfig
	if _, statErr := os.Stat(path); statErr == nil {
		if err := loadYAML(path, &cfg); err != nil {
			return nil, err
		}
	}
	if err := resolveSourcePaths(path, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// resolveSourcePaths applies the browser/Chrome-path/cmux-path resolution
// shared by LoadSource and LoadSourceLocal (everything except the
// push-only sink/peer/secret validation).
func resolveSourcePaths(path string, cfg *SourceConfig) error {
	cfg.Chrome.DBPath = ExpandTilde(cfg.Chrome.DBPath)
	if cfg.Browser.Name != "" {
		if _, err := lookupSourceBrowserPath(cfg.Browser.Name); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	if cfg.Chrome.DBPath == "" {
		if cfg.Browser.Name != "" || cfg.Browser.Profile != "" {
			dbPath, err := SourceBrowserCookiesPath(cfg.Browser.Name, cfg.Browser.Profile)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			cfg.Chrome.DBPath = dbPath
		} else {
			cfg.Chrome.DBPath = DefaultChromeCookiesPath()
		}
	}
	if cfg.Cmux.CmuxPath != "" {
		cfg.Cmux.CmuxPath = ExpandTilde(cfg.Cmux.CmuxPath)
	}
	return nil
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
	if cfg.Cmux.CmuxPath != "" {
		cfg.Cmux.CmuxPath = ExpandTilde(cfg.Cmux.CmuxPath)
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
	path, _ := SourceBrowserCookiesPath(defaultBrowserName, defaultBrowserProfile)
	return path
}

// SourceBrowserCookiesPath returns the Cookies SQLite path for the configured
// source browser. Empty name/profile default to Chrome/Default.
func SourceBrowserCookiesPath(name, profile string) (string, error) {
	ref, err := lookupSourceBrowserPath(name)
	if err != nil {
		return "", err
	}
	if profile == "" {
		profile = defaultBrowserProfile
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	parts := []string{home, "Library", "Application Support"}
	parts = append(parts, ref.SupportDir...)
	parts = append(parts, profile, "Cookies")
	return filepath.Join(parts...), nil
}

func lookupSourceBrowserPath(name string) (browserPathRef, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = defaultBrowserName
	}
	ref, ok := sourceBrowserPaths[key]
	if !ok {
		return browserPathRef{}, fmt.Errorf("unsupported browser %q (supported: %s)", name, strings.Join(SupportedBrowserNames(), ", "))
	}
	ref.SupportDir = append([]string(nil), ref.SupportDir...)
	return ref, nil
}

// SupportedBrowserNames returns the source-browser adapter names accepted by
// source.yaml.
func SupportedBrowserNames() []string {
	names := make([]string, 0, len(sourceBrowserPaths))
	for name := range sourceBrowserPaths {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
