package config

import (
	"os"
	"path/filepath"
	"runtime"
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
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
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

func TestLoadSourceCDPSourceParsesAndRejectsUnsafeEndpoint(t *testing.T) {
	t.Run("loopback endpoint", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
peer:
  hostname: sink
cdp_source:
  enabled: true
  endpoint: http://127.0.0.1:9230
`)
		cfg, err := LoadSource(dir)
		if err != nil {
			t.Fatalf("LoadSource: %v", err)
		}
		if !cfg.CDPSource.Enabled || cfg.CDPSource.Endpoint != "http://127.0.0.1:9230" {
			t.Fatalf("CDPSource = %+v", cfg.CDPSource)
		}
	})

	t.Run("non-loopback endpoint", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
peer:
  hostname: sink
cdp_source:
  enabled: true
  endpoint: http://100.91.16.115:9230
`)
		if _, err := LoadSource(dir); err == nil {
			t.Fatal("LoadSource accepted a non-loopback cdp_source endpoint")
		}
	})

	t.Run("rejects SQLite browser configuration", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
peer:
  hostname: sink
chrome:
  db_path: /private/Cookies
cdp_source:
  enabled: true
  endpoint: http://127.0.0.1:9230
`)
		if _, err := LoadSource(dir); err == nil {
			t.Fatal("LoadSource accepted cdp_source combined with chrome.db_path")
		}
	})
}

func TestLoadSourceLocalAcceptsCDPSource(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
cdp_source:
  enabled: true
  endpoint: http://127.0.0.1:9230
`)

	cfg, err := LoadSourceLocal(dir)
	if err != nil {
		t.Fatalf("LoadSourceLocal CDP source: %v", err)
	}
	if !cfg.CDPSource.Enabled {
		t.Fatal("LoadSourceLocal did not retain cdp_source")
	}
}

func TestLoadSourceBrowserBlockParsesAndDerivesPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
browser:
  name: atlas
  profile: Profile 1
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if cfg.Browser.Name != "atlas" || cfg.Browser.Profile != "Profile 1" {
		t.Errorf("browser ref: got %+v", cfg.Browser)
	}
	home, _ := os.UserHomeDir()
	// Atlas paths are the same on macOS and Linux (Atlas is macOS-only,
	// but the path derivation code passes the macOS segments through on Linux
	// since there's no Linux equivalent).
	var want string
	if runtime.GOOS == "linux" {
		want = filepath.Join(home, ".config", "com.openai.atlas", "browser-data", "host", "Profile 1", "Cookies")
	} else {
		want = filepath.Join(home, "Library", "Application Support", "com.openai.atlas", "browser-data", "host", "Profile 1", "Cookies")
	}
	if cfg.Chrome.DBPath != want {
		t.Errorf("derived DBPath: got %q, want %q", cfg.Chrome.DBPath, want)
	}
}

func TestLoadSourceBrowserDiaDerivesUserDataPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
browser:
  name: dia
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if cfg.Browser.Name != "dia" {
		t.Errorf("browser name: got %q, want dia", cfg.Browser.Name)
	}
	home, _ := os.UserHomeDir()
	var want string
	if runtime.GOOS == "linux" {
		want = filepath.Join(home, ".config", "Dia", "User Data", "Default", "Cookies")
	} else {
		want = filepath.Join(home, "Library", "Application Support", "Dia", "User Data", "Default", "Cookies")
	}
	if cfg.Chrome.DBPath != want {
		t.Errorf("derived DBPath: got %q, want %q", cfg.Chrome.DBPath, want)
	}
}

func TestLoadSourceDBPathOverridesBrowserDerivedPath(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "Custom", "Cookies")
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
chrome:
  db_path: `+explicit+`
browser:
  name: atlas
  profile: Profile 1
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if cfg.Chrome.DBPath != explicit {
		t.Errorf("explicit db_path should win: got %q, want %q", cfg.Chrome.DBPath, explicit)
	}
	if cfg.Browser.Name != "atlas" {
		t.Errorf("browser name should remain available for keychain selection, got %q", cfg.Browser.Name)
	}
}

func TestLoadSourceWithoutBrowserDefaultsChrome(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if cfg.Chrome.DBPath != DefaultChromeCookiesPath() {
		t.Errorf("default DBPath: got %q, want %q", cfg.Chrome.DBPath, DefaultChromeCookiesPath())
	}
}

func TestLoadSourceUnknownBrowserFailsWithSupportedNames(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
browser:
  name: vivaldi
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	_, err := LoadSource(dir)
	if err == nil {
		t.Fatal("expected unsupported browser error")
	}
	if !strings.Contains(err.Error(), "supported:") || !strings.Contains(err.Error(), "chrome") {
		t.Errorf("error should list supported browsers, got %v", err)
	}
}

func TestLoadSourceMissingSinkURL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
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

func TestLoadSinkEmptyListenIsError(t *testing.T) {
	// v0.12 S1: a sink.yaml without listen.addr used to fall through to
	// 127.0.0.1:9999. That made the wizard's silent-detection-failure
	// path one layer harder to spot. Now empty is a config error and the
	// wizard install is the only place that writes the address.
	dir := t.TempDir()
	writeFile(t, dir, "sink.yaml", `
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	if _, err := LoadSink(dir); err == nil {
		t.Fatal("expected error for missing listen.addr, got nil")
	} else if !strings.Contains(err.Error(), "listen.addr is required") {
		t.Errorf("error should name listen.addr, got %v", err)
	}
}

// TestLoadSinkSkipChromeSQLite covers the v0.12.0-beta.3 headless mode.
// Round-trips skip_chrome_sqlite + cdp.enabled through YAML and checks
// that absence defaults to legacy behavior (R6 regression guard).
func TestLoadSinkSkipChromeSQLite(t *testing.T) {
	t.Run("skip_chrome_sqlite true and cdp enabled", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
skip_chrome_sqlite: true
cdp:
  enabled: true
  profile_dir: ~/.agentcookie/chrome-profile
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if !cfg.SkipChromeSQLite {
			t.Errorf("SkipChromeSQLite: got false, want true")
		}
		if !cfg.CDP.Enabled {
			t.Errorf("CDP.Enabled: got false, want true")
		}
		if cfg.CDP.ProfileDir != "~/.agentcookie/chrome-profile" {
			t.Errorf("CDP.ProfileDir: got %q", cfg.CDP.ProfileDir)
		}
	})

	t.Run("absent fields default to legacy behavior", func(t *testing.T) {
		// R6 regression guard: a v0.12.0-beta.2 sink.yaml that does NOT
		// mention skip_chrome_sqlite or cdp must keep the old defaults
		// (false for both on Darwin), so installed friends see no behavior change.
		// NOTE: On Linux, skip_chrome_sqlite defaults to true (no Keychain).
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		// Platform-specific defaults: Linux defaults to skip_chrome_sqlite=true
		// and live_cdp.enabled=true; Darwin keeps legacy false defaults.
		if IsDarwin() {
			if cfg.SkipChromeSQLite {
				t.Errorf("SkipChromeSQLite: got true, want false (legacy default on Darwin)")
			}
			if cfg.CDP.Enabled {
				t.Errorf("CDP.Enabled: got true, want false (legacy default)")
			}
		} else if IsLinux() {
			if !cfg.SkipChromeSQLite {
				t.Errorf("SkipChromeSQLite: got false, want true (Linux default)")
			}
			if !cfg.LiveCDP.Enabled {
				t.Errorf("LiveCDP.Enabled: got false, want true (Linux default)")
			}
		}
	})
}

// TestLoadSinkDeliveryMarker covers the v0.13 universal-cookie-delivery
// marker. The delivery field round-trips through YAML so a later doctor
// unit can report intent, and its absence keeps current behavior (no
// migration, no silent flip on a binary upgrade).
func TestLoadSinkDeliveryMarker(t *testing.T) {
	t.Run("delivery universal round-trips", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
delivery: universal
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if cfg.Delivery != "universal" {
			t.Errorf("Delivery: got %q, want %q", cfg.Delivery, "universal")
		}
		// On Darwin, universal delivery should not skip Chrome SQLite.
		// On Linux, skip_chrome_sqlite is always true (no Keychain).
		if IsDarwin() && cfg.SkipChromeSQLite {
			t.Errorf("universal install on Darwin should not skip Chrome SQLite")
		}
	})

	t.Run("delivery degraded round-trips", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
skip_chrome_sqlite: true
delivery: degraded
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if cfg.Delivery != "degraded" {
			t.Errorf("Delivery: got %q, want %q", cfg.Delivery, "degraded")
		}
	})

	t.Run("absent delivery defaults to empty (no migration)", func(t *testing.T) {
		// A sink.yaml written before this field must load cleanly with an
		// empty Delivery and unchanged behavior -- no silent flip.
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if cfg.Delivery != "" {
			t.Errorf("Delivery: got %q, want empty (legacy default)", cfg.Delivery)
		}
	})
}

func TestLoadSourceLocal(t *testing.T) {
	t.Run("source.yaml without sink/peer loads for local loop", func(t *testing.T) {
		// The pure local-loop case: no sink, no peer/secret. LoadSource
		// would reject this; LoadSourceLocal must accept it.
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
chrome:
  db_path: ~/cookies/Cookies
cmux:
  enabled: true
`)
		cfg, err := LoadSourceLocal(dir)
		if err != nil {
			t.Fatalf("LoadSourceLocal: %v", err)
		}
		if !cfg.Cmux.Enabled {
			t.Errorf("Cmux.Enabled: got false, want true")
		}
		if strings.HasPrefix(cfg.Chrome.DBPath, "~") {
			t.Errorf("Chrome.DBPath should be tilde-expanded, got %q", cfg.Chrome.DBPath)
		}
	})

	t.Run("missing source.yaml yields defaults, no error", func(t *testing.T) {
		dir := t.TempDir() // empty
		cfg, err := LoadSourceLocal(dir)
		if err != nil {
			t.Fatalf("LoadSourceLocal with no source.yaml: %v", err)
		}
		if cfg.Chrome.DBPath == "" {
			t.Errorf("Chrome.DBPath should default, got empty")
		}
		if cfg.Cmux.Enabled {
			t.Errorf("Cmux should default to disabled")
		}
	})

	t.Run("LoadSource still requires sink (regression)", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
chrome:
  db_path: ~/cookies/Cookies
`)
		if _, err := LoadSource(dir); err == nil {
			t.Fatal("LoadSource should still require sink.url")
		}
	})

	t.Run("accepts CDP source for CDP-capable local commands", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
cdp_source:
  enabled: true
  endpoint: http://127.0.0.1:9230
`)
		cfg, err := LoadSourceLocal(dir)
		if err != nil {
			t.Fatalf("LoadSourceLocal CDP source: %v", err)
		}
		if !cfg.CDPSource.Enabled {
			t.Fatal("LoadSourceLocal did not retain CDP source configuration")
		}
	})
}

func TestLoadSourceCmuxLoop(t *testing.T) {
	t.Run("enabled cmux loop round-trips with tilde-expanded path", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
sink:
  url: https://100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
cmux:
  enabled: true
  cmux_path: ~/bin/cmux
  domain_filter:
    - "%github.com"
`)
		cfg, err := LoadSource(dir)
		if err != nil {
			t.Fatalf("LoadSource: %v", err)
		}
		if !cfg.Cmux.Enabled {
			t.Errorf("Cmux.Enabled: got false, want true")
		}
		if strings.HasPrefix(cfg.Cmux.CmuxPath, "~") {
			t.Errorf("Cmux.CmuxPath should be tilde-expanded, got %q", cfg.Cmux.CmuxPath)
		}
		if len(cfg.Cmux.DomainFilter) != 1 || cfg.Cmux.DomainFilter[0] != "%github.com" {
			t.Errorf("Cmux.DomainFilter: got %v", cfg.Cmux.DomainFilter)
		}
	})

	t.Run("absent cmux block defaults to disabled", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "source.yaml", `
sink:
  url: https://100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSource(dir)
		if err != nil {
			t.Fatalf("LoadSource: %v", err)
		}
		if cfg.Cmux.Enabled {
			t.Errorf("Cmux.Enabled: got true, want false (legacy default)")
		}
	})
}

func TestLoadSinkCmuxSurface(t *testing.T) {
	t.Run("enabled cmux block round-trips with filter and tilde-expanded path", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
cmux:
  enabled: true
  cmux_path: ~/bin/cmux
  domain_filter:
    - "%github.com"
    - "%openai.com"
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if !cfg.Cmux.Enabled {
			t.Errorf("Cmux.Enabled: got false, want true")
		}
		if strings.HasPrefix(cfg.Cmux.CmuxPath, "~") {
			t.Errorf("Cmux.CmuxPath should be tilde-expanded, got %q", cfg.Cmux.CmuxPath)
		}
		if len(cfg.Cmux.DomainFilter) != 2 || cfg.Cmux.DomainFilter[0] != "%github.com" {
			t.Errorf("Cmux.DomainFilter: got %v", cfg.Cmux.DomainFilter)
		}
	})

	t.Run("absent cmux block defaults to disabled (no migration)", func(t *testing.T) {
		// A sink.yaml written before this field must load cleanly with the
		// surface off -- no silent flip on a binary upgrade.
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		cfg, err := LoadSink(dir)
		if err != nil {
			t.Fatalf("LoadSink: %v", err)
		}
		if cfg.Cmux.Enabled {
			t.Errorf("Cmux.Enabled: got true, want false (legacy default)")
		}
	})

	t.Run("unknown key under cmux is rejected by KnownFields", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
cmux:
  enabled: true
  bogus_key: nope
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
		if _, err := LoadSink(dir); err == nil {
			t.Fatal("expected error for unknown cmux key, got nil")
		}
	})
}

func TestLoadSinkHonorsExplicitListenAddr(t *testing.T) {
	// Regression for v0.11 -> v0.12: an existing sink.yaml that already
	// has a 100.x address keeps working without re-detection prompting.
	dir := t.TempDir()
	writeFile(t, dir, "sink.yaml", `
listen:
  addr: 100.80.229.80:9999
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	cfg, err := LoadSink(dir)
	if err != nil {
		t.Fatalf("LoadSink: %v", err)
	}
	if cfg.Listen.Addr != "100.80.229.80:9999" {
		t.Errorf("listen addr: got %q", cfg.Listen.Addr)
	}
}

// TestLoadSourceRejectsShortSharedSecret covers U10: a legacy
// security.shared_secret below 32 bytes is now refused at config
// load. The error names the byte count and points at pairing.
func TestLoadSourceRejectsShortSharedSecret(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
security:
  shared_secret: tooshort
`)
	_, err := LoadSource(dir)
	if err == nil {
		t.Fatal("expected error for short shared_secret, got nil")
	}
	if !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Errorf("error should name the 32-byte floor, got %v", err)
	}
	if !strings.Contains(err.Error(), "agentcookie pair") {
		t.Errorf("error should suggest pairing, got %v", err)
	}
}

// TestLoadSourceAcceptsExactly32ByteSecret proves the entropy floor
// is inclusive at exactly 32 bytes (matches the AES-256 key length).
func TestLoadSourceAcceptsExactly32ByteSecret(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sink:
  url: http://example.test:9999/sync
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`)
	if _, err := LoadSource(dir); err != nil {
		t.Errorf("32-byte shared_secret should be accepted, got %v", err)
	}
}

func TestLoadBlocklistHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 1
domains:
  - pattern: "chase.com"
    description: Chase bank
  - pattern: "%.chase.com"
`)
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(bl.Domains) != 2 {
		t.Errorf("expected 2 domains, got %d", len(bl.Domains))
	}
	if bl.Domains[0].Pattern != "chase.com" {
		t.Errorf("first pattern wrong: %q", bl.Domains[0].Pattern)
	}
	if bl.PolicyMode() != CookiePolicyBlocklist {
		t.Errorf("omitted policy = %q, want blocklist", bl.PolicyMode())
	}
}

func TestLoadBlocklistExplicitPolicies(t *testing.T) {
	t.Run("blocklist", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "blocklist.yaml", `
version: 1
policy: blocklist
domains:
  - pattern: "chase.com"
`)
		bl, err := LoadBlocklist(dir)
		if err != nil {
			t.Fatalf("LoadBlocklist: %v", err)
		}
		if bl.PolicyMode() != CookiePolicyBlocklist {
			t.Errorf("PolicyMode = %q, want blocklist", bl.PolicyMode())
		}
		if bl.CookiePolicySummary() != "blocklist" {
			t.Errorf("summary = %q, want blocklist", bl.CookiePolicySummary())
		}
	})
	t.Run("allowlist", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "blocklist.yaml", `
version: 1
policy: allowlist
domains:
  - pattern: "youtube.com"
  - pattern: "%.youtube.com"
`)
		bl, err := LoadBlocklist(dir)
		if err != nil {
			t.Fatalf("LoadBlocklist: %v", err)
		}
		if bl.PolicyMode() != CookiePolicyAllowlist {
			t.Errorf("PolicyMode = %q, want allowlist", bl.PolicyMode())
		}
		if bl.CookiePolicySummary() != "allowlist" {
			t.Errorf("summary = %q, want allowlist", bl.CookiePolicySummary())
		}
	})
}

func TestLoadBlocklistMissingReturnsEmpty(t *testing.T) {
	// v0.3: missing blocklist is NOT an error. Empty = sync-all default.
	dir := t.TempDir()
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist on missing file should not error: %v", err)
	}
	if bl == nil || len(bl.Domains) != 0 {
		t.Errorf("expected empty blocklist, got %+v", bl)
	}
	if bl.CookiePolicySummary() != "sync-all" {
		t.Errorf("missing blocklist summary = %q, want sync-all", bl.CookiePolicySummary())
	}
}

func TestPolicyModeForSink(t *testing.T) {
	t.Run("explicit blocklist policy", func(t *testing.T) {
		bl := &Blocklist{Version: 1, Policy: CookiePolicyBlocklist}
		if bl.PolicyModeForSink() != CookiePolicyBlocklist {
			t.Errorf("explicit blocklist should return blocklist, got %s", bl.PolicyModeForSink())
		}
	})

	t.Run("explicit allowlist policy", func(t *testing.T) {
		bl := &Blocklist{Version: 1, Policy: CookiePolicyAllowlist}
		if bl.PolicyModeForSink() != CookiePolicyAllowlist {
			t.Errorf("explicit allowlist should return allowlist, got %s", bl.PolicyModeForSink())
		}
	})

	t.Run("omitted policy", func(t *testing.T) {
		bl := &Blocklist{Version: 1}
		mode := bl.PolicyModeForSink()
		// On Linux, omitted policy defaults to allowlist (security)
		// On Darwin, omitted policy defaults to blocklist (legacy)
		if IsLinux() && mode != CookiePolicyAllowlist {
			t.Errorf("on Linux, omitted policy should return allowlist, got %s", mode)
		}
		if IsDarwin() && mode != CookiePolicyBlocklist {
			t.Errorf("on Darwin, omitted policy should return blocklist, got %s", mode)
		}
	})

	t.Run("nil blocklist", func(t *testing.T) {
		var bl *Blocklist
		mode := bl.PolicyModeForSink()
		if IsLinux() && mode != CookiePolicyAllowlist {
			t.Errorf("on Linux, nil blocklist should return allowlist, got %s", mode)
		}
		if IsDarwin() && mode != CookiePolicyBlocklist {
			t.Errorf("on Darwin, nil blocklist should return blocklist, got %s", mode)
		}
	})
}

func TestLoadBlocklistRejectsUnknownPolicy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 1
policy: denylist
domains: []
`)
	_, err := LoadBlocklist(dir)
	if err == nil {
		t.Fatal("expected error for unknown policy, got nil")
	}
	if !strings.Contains(err.Error(), "blocklist.yaml") || !strings.Contains(err.Error(), "policy") {
		t.Errorf("error should name file and field, got %v", err)
	}
}

func TestLoadBlocklistRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 99
domains: []
`)
	if _, err := LoadBlocklist(dir); err == nil {
		t.Fatal("expected error for unknown version, got nil")
	}
}

func TestLoadBlocklistRejectsEmptyPattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "blocklist.yaml", `
version: 1
domains:
  - pattern: ""
    description: oops
`)
	if _, err := LoadBlocklist(dir); err == nil {
		t.Fatal("expected error for empty pattern, got nil")
	}
}

func TestLoadBlocklistMigratesLegacyAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "allowlist.yaml", `
version: 1
domains:
  - pattern: "%.instacart.com"
`)
	bl, err := LoadBlocklist(dir)
	if err != nil {
		t.Fatalf("LoadBlocklist: %v", err)
	}
	if len(bl.Domains) != 0 {
		t.Errorf("legacy allowlist should NOT carry over to blocklist; got %d domains", len(bl.Domains))
	}
	// Legacy file should be renamed to .v2.bak.
	if _, err := os.Stat(filepath.Join(dir, "allowlist.yaml.v2.bak")); err != nil {
		t.Errorf("legacy allowlist should be renamed to .v2.bak, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "allowlist.yaml")); !os.IsNotExist(err) {
		t.Errorf("legacy allowlist should be gone after migration, got %v", err)
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

func TestLoadSourceMultiSinkResolvesAllEntries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sinks:
  - url: http://a.test:9999/sync
    peer: alpha
  - url: http://b.test:9999/sync
    peer: bravo
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
`)
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	got := cfg.ResolvedSinks()
	if len(got) != 2 {
		t.Fatalf("expected 2 resolved sinks, got %d", len(got))
	}
	if got[0].URL != "http://a.test:9999/sync" || got[0].Peer != "alpha" {
		t.Errorf("sink[0] wrong: %+v", got[0])
	}
	if got[1].URL != "http://b.test:9999/sync" || got[1].Peer != "bravo" {
		t.Errorf("sink[1] wrong: %+v", got[1])
	}
}

func TestResolvedSinksSynthesizesLegacyScalar(t *testing.T) {
	cfg := &SourceConfig{
		Sink: SinkRef{URL: "http://legacy.test:9999/sync"},
		Peer: PeerRef{Hostname: "legacy-peer"},
	}
	got := cfg.ResolvedSinks()
	if len(got) != 1 {
		t.Fatalf("expected 1 synthesized sink, got %d", len(got))
	}
	if got[0].URL != "http://legacy.test:9999/sync" || got[0].Peer != "legacy-peer" {
		t.Errorf("synthesized sink wrong: %+v", got[0])
	}
}

func TestResolvedSinksPrefersSinksOverLegacyScalar(t *testing.T) {
	cfg := &SourceConfig{
		Sinks: []SinkTarget{{URL: "http://new.test:9999/sync", Peer: "new-peer"}},
		Sink:  SinkRef{URL: "http://legacy.test:9999/sync"},
		Peer:  PeerRef{Hostname: "legacy-peer"},
	}
	got := cfg.ResolvedSinks()
	if len(got) != 1 || got[0].Peer != "new-peer" {
		t.Fatalf("expected sinks list to win, got %+v", got)
	}
}

func TestResolvedSinksEmptyWhenNeitherSet(t *testing.T) {
	cfg := &SourceConfig{}
	if got := cfg.ResolvedSinks(); got != nil {
		t.Fatalf("expected nil for no sinks, got %+v", got)
	}
}

func TestLoadSourceMultiSinkRequiresPerSinkPeerOrSharedSecret(t *testing.T) {
	dir := t.TempDir()
	// A sinks entry with no peer and no legacy shared secret has no way to seal.
	writeFile(t, dir, "source.yaml", `
sinks:
  - url: http://a.test:9999/sync
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
`)
	if _, err := LoadSource(dir); err == nil {
		t.Fatal("expected error for a sink with no peer and no shared secret, got nil")
	}
}

func TestLoadSourceMultiSinkSharedSecretCoversPeerlessSink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "source.yaml", `
sinks:
  - url: http://a.test:9999/sync
security:
  shared_secret: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
chrome:
  db_path: ~/Library/Application Support/Google/Chrome/Default/Cookies
`)
	if _, err := LoadSource(dir); err != nil {
		t.Fatalf("shared secret should cover a peerless sink: %v", err)
	}
}

func TestExampleMultiSinkConfigDecodes(t *testing.T) {
	// The shipped multi-sink example must load through the strict loader,
	// so the docs never drift from the accepted schema.
	dir := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "source-multi-sink.yaml"))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.yaml"), data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadSource(dir)
	if err != nil {
		t.Fatalf("example source-multi-sink.yaml should load: %v", err)
	}
	if got := len(cfg.ResolvedSinks()); got != 2 {
		t.Fatalf("example should declare 2 sinks, got %d", got)
	}
}
