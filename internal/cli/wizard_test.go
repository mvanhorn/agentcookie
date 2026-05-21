package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderSinkYAML_WritesResolvedAddr proves the wizard pipes the
// resolved tailnet IP into sink.yaml verbatim. Pre-v0.12 the render
// helper called net.InterfaceAddrs directly and fell through to
// 0.0.0.0:9999 on failure; the v0.12 shape takes the address as an
// argument so the call site can call tsclient.RequireTailnetIP and
// fail loud before we ever reach this helper.
func TestRenderSinkYAML_WritesResolvedAddr(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "")
	if !strings.Contains(got, "addr: 100.80.229.80:9999") {
		t.Errorf("expected listen.addr in YAML, got:\n%s", got)
	}
	if !strings.Contains(got, "hostname: my-laptop") {
		t.Errorf("expected peer.hostname in YAML, got:\n%s", got)
	}
	if strings.Contains(got, "0.0.0.0") {
		t.Errorf("v0.12: sink.yaml must never carry 0.0.0.0; got:\n%s", got)
	}
}

// TestRenderSinkYAML_LegacyShapeStable is the R6 regression guard for
// v0.12.0-beta.3: when skipChromeSQLite and cdpEnabled are both false,
// the rendered YAML must match the pre-beta.3 shape byte-for-byte.
// Installed v0.12.0-beta.2 friends upgrading the binary in place must
// see no config-level behavior change.
func TestRenderSinkYAML_LegacyShapeStable(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "")
	want := "listen:\n  addr: 100.80.229.80:9999\npeer:\n  hostname: my-laptop\n"
	if got != want {
		t.Errorf("legacy YAML drift:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderSinkYAML_HeadlessMode covers the v0.12.0-beta.3 default
// shape: skip_chrome_sqlite + cdp block both emitted when the wizard
// resolves headless mode.
func TestRenderSinkYAML_HeadlessMode(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", true, true, "~/.agentcookie/chrome-profile")
	if !strings.Contains(got, "skip_chrome_sqlite: true") {
		t.Errorf("expected skip_chrome_sqlite: true in YAML, got:\n%s", got)
	}
	if !strings.Contains(got, "cdp:") || !strings.Contains(got, "enabled: true") {
		t.Errorf("expected cdp.enabled: true in YAML, got:\n%s", got)
	}
	if !strings.Contains(got, "profile_dir: ~/.agentcookie/chrome-profile") {
		t.Errorf("expected cdp.profile_dir in YAML, got:\n%s", got)
	}
}

// TestRenderSinkYAML_SkipSQLiteWithoutCDP covers the --no-cdp opt-out
// path: skip_chrome_sqlite is emitted but no cdp block. The friend
// wanted sidecar+adapter only.
func TestRenderSinkYAML_SkipSQLiteWithoutCDP(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", true, false, "")
	if !strings.Contains(got, "skip_chrome_sqlite: true") {
		t.Errorf("expected skip_chrome_sqlite: true, got:\n%s", got)
	}
	if strings.Contains(got, "cdp:") {
		t.Errorf("--no-cdp path must NOT emit cdp block, got:\n%s", got)
	}
}

// TestResolveSinkHeadlessMode_FlagPrecedence covers the explicit-flag
// precedence rules in resolveSinkHeadlessMode. Headless auto-detect
// only applies when neither --skip-chrome-sqlite nor --write-chrome-sqlite
// was passed.
func TestResolveSinkHeadlessMode_FlagPrecedence(t *testing.T) {
	saveSkip, saveWrite, saveNoCDP := wizardSkipChromeSQLite, wizardWriteChromeSQLite, wizardNoCDP
	defer func() {
		wizardSkipChromeSQLite = saveSkip
		wizardWriteChromeSQLite = saveWrite
		wizardNoCDP = saveNoCDP
	}()

	t.Run("explicit --skip-chrome-sqlite wins", func(t *testing.T) {
		wizardSkipChromeSQLite = true
		wizardWriteChromeSQLite = false
		wizardNoCDP = false
		skip, cdp, _ := resolveSinkHeadlessMode()
		if !skip {
			t.Errorf("--skip-chrome-sqlite should set skip=true")
		}
		if !cdp {
			t.Errorf("skip=true with --no-cdp absent should set cdp=true")
		}
	})

	t.Run("explicit --write-chrome-sqlite wins", func(t *testing.T) {
		wizardSkipChromeSQLite = false
		wizardWriteChromeSQLite = true
		wizardNoCDP = false
		skip, cdp, _ := resolveSinkHeadlessMode()
		if skip {
			t.Errorf("--write-chrome-sqlite should set skip=false")
		}
		if cdp {
			t.Errorf("skip=false should set cdp=false")
		}
	})

	t.Run("--no-cdp disables cdp on headless install", func(t *testing.T) {
		wizardSkipChromeSQLite = true
		wizardWriteChromeSQLite = false
		wizardNoCDP = true
		skip, cdp, _ := resolveSinkHeadlessMode()
		if !skip {
			t.Errorf("--skip-chrome-sqlite should still set skip=true with --no-cdp")
		}
		if cdp {
			t.Errorf("--no-cdp should set cdp=false")
		}
	})
}

// TestValidateListenAddr_AcceptsExplicitOperatorInput is the regression
// guard for the wizard's --listen flag. An operator passing an
// explicit value (during local dev or for an unusual deployment) must
// be allowed through if it matches the policy. The empty-flag path is
// the one that auto-detects; this test covers the explicit path.
func TestValidateListenAddr_AcceptsExplicitOperatorInput(t *testing.T) {
	ok := []string{
		"100.80.229.80:9998",
		"127.0.0.1:9998",
		"localhost:9998",
	}
	for _, addr := range ok {
		if err := validateListenAddr(addr); err != nil {
			t.Errorf("validateListenAddr(%q) unexpectedly errored: %v", addr, err)
		}
	}

	refused := map[string]string{
		"0.0.0.0:9998":   "every interface",
		"192.168.1.5:9998": "not a Tailscale 100.x address",
	}
	for addr, want := range refused {
		err := validateListenAddr(addr)
		if err == nil {
			t.Errorf("validateListenAddr(%q) should have errored", addr)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateListenAddr(%q): error %v, want substring %q", addr, err, want)
		}
	}
}

// TestGuardConfigPeerMismatch is the regression guard for friction #14
// (2026-05-19 dry-run). Re-running wizard install with a --peer that
// differs from the existing sink.yaml peer.hostname used to silently
// keep the stale config and produce broken sync after the next pair
// handshake. The guard now errors out unless --force is passed.
func TestGuardConfigPeerMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sink.yaml")
	yaml := []byte("listen:\n  addr: 100.80.229.80:9999\npeer:\n  hostname: old-name\n")
	if err := os.WriteFile(path, yaml, 0o600); err != nil {
		t.Fatal(err)
	}

	// Matching peer: no error.
	if err := guardConfigPeerMismatch("sink", path, "old-name"); err != nil {
		t.Errorf("matching peer should not error, got: %v", err)
	}

	// Mismatching peer without --force: error pointing at remediation.
	prev := wizardForce
	wizardForce = false
	defer func() { wizardForce = prev }()
	err := guardConfigPeerMismatch("sink", path, "new-name")
	if err == nil {
		t.Fatal("mismatching peer without --force should error")
	}
	if !strings.Contains(err.Error(), "old-name") || !strings.Contains(err.Error(), "new-name") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention old, new, and --force; got: %v", err)
	}

	// Mismatching peer with --force: no error (caller writes the new YAML).
	wizardForce = true
	if err := guardConfigPeerMismatch("sink", path, "new-name"); err != nil {
		t.Errorf("mismatching peer with --force should not error, got: %v", err)
	}

	// Missing file: no error (writeYAMLIfMissing will write fresh).
	missing := filepath.Join(dir, "missing.yaml")
	if err := guardConfigPeerMismatch("sink", missing, "any-name"); err != nil {
		t.Errorf("missing file should not error, got: %v", err)
	}
}
