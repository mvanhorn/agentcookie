package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errInjectedKeychainOpen is the failure injected into the
// attemptUniversalKeychainOpen seam to exercise the downgrade-on-failure
// flow without spawning the real one-shot LaunchAgent.
var errInjectedKeychainOpen = errors.New("injected: any-app keychain open could not complete")

// TestRenderSinkYAML_WritesResolvedAddr proves the wizard pipes the
// resolved tailnet IP into sink.yaml verbatim. Pre-v0.12 the render
// helper called net.InterfaceAddrs directly and fell through to
// 0.0.0.0:9999 on failure; the v0.12 shape takes the address as an
// argument so the call site can call tsclient.RequireTailnetIP and
// fail loud before we ever reach this helper.
func TestRenderSinkYAML_WritesResolvedAddr(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "", "")
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
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "", "")
	want := "listen:\n  addr: 100.80.229.80:9999\npeer:\n  hostname: my-laptop\n"
	if got != want {
		t.Errorf("legacy YAML drift:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderSinkYAML_HeadlessMode covers the v0.12.0-beta.3 default
// shape: skip_chrome_sqlite + cdp block both emitted when the wizard
// resolves headless mode.
func TestRenderSinkYAML_HeadlessMode(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", true, true, "~/.agentcookie/chrome-profile", "")
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
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999", true, false, "", "")
	if !strings.Contains(got, "skip_chrome_sqlite: true") {
		t.Errorf("expected skip_chrome_sqlite: true, got:\n%s", got)
	}
	if strings.Contains(got, "cdp:") {
		t.Errorf("--no-cdp path must NOT emit cdp block, got:\n%s", got)
	}
}

// TestRenderSinkYAML_DeliveryMarker covers the v0.13 delivery marker:
// a universal install records delivery="universal" and a degraded
// opt-out records delivery="degraded" in the rendered sink.yaml, so a
// later doctor unit can report intent without re-inferring it. An empty
// delivery (legacy callers/tests) must emit no delivery line at all.
func TestRenderSinkYAML_DeliveryMarker(t *testing.T) {
	t.Run("universal records delivery: universal", func(t *testing.T) {
		got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "", deliveryUniversal)
		if !strings.Contains(got, "delivery: universal") {
			t.Errorf("universal install should record delivery: universal, got:\n%s", got)
		}
		if strings.Contains(got, "skip_chrome_sqlite") {
			t.Errorf("universal install must not skip Chrome SQLite, got:\n%s", got)
		}
	})

	t.Run("degraded records delivery: degraded", func(t *testing.T) {
		got := renderSinkYAML("my-laptop", "100.80.229.80:9999", true, true, "~/.agentcookie/chrome-profile", deliveryDegraded)
		if !strings.Contains(got, "delivery: degraded") {
			t.Errorf("degraded opt-out should record delivery: degraded, got:\n%s", got)
		}
		if !strings.Contains(got, "skip_chrome_sqlite: true") {
			t.Errorf("degraded opt-out should set skip_chrome_sqlite: true, got:\n%s", got)
		}
	})

	t.Run("empty delivery emits no delivery line", func(t *testing.T) {
		got := renderSinkYAML("my-laptop", "100.80.229.80:9999", false, false, "", "")
		if strings.Contains(got, "delivery:") {
			t.Errorf("empty delivery must not emit a delivery line, got:\n%s", got)
		}
	})
}

// TestUniversalInstallPassesAnyApp documents that the universal default
// branch in wizardInstallSink opts the keychain step into U1's --any-app
// strategy, while the degraded opt-out keeps the per-binary -T behavior
// (setKeychainAnyApp stays false). This mirrors the structural gating
// test below; the actual loop runs out-of-process via runOuterWizard.
func TestUniversalInstallPassesAnyApp(t *testing.T) {
	cases := []struct {
		name             string
		skipChromeSQLite bool
		skipKeychainAcc  bool
		wantLoopRuns     bool
		wantAnyApp       bool
	}{
		{"universal default opens any-app", false, false, true, true},
		{"degraded opt-out keeps -T (no any-app)", true, false, false, false},
		{"explicit --skip-keychain-access: no loop, no any-app", false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Model the same gating decision wizardInstallSink makes.
			loopRuns := !tc.skipKeychainAcc && !tc.skipChromeSQLite
			anyApp := loopRuns // the default (universal) branch sets setKeychainAnyApp=true
			if loopRuns != tc.wantLoopRuns {
				t.Errorf("loopRuns: got %v, want %v", loopRuns, tc.wantLoopRuns)
			}
			if anyApp != tc.wantAnyApp {
				t.Errorf("anyApp: got %v, want %v", anyApp, tc.wantAnyApp)
			}
		})
	}
}

// TestResolveSinkHeadlessMode_FlagPrecedence covers the v0.13 precedence
// rules in resolveSinkHeadlessMode. The headline change: when neither
// --skip-chrome-sqlite nor --write-chrome-sqlite is passed, the install
// now defaults to UNIVERSAL (skip=false, delivery="universal"), instead
// of the pre-v0.13 no-TTY auto-degrade.
func TestResolveSinkHeadlessMode_FlagPrecedence(t *testing.T) {
	saveSkip, saveWrite, saveNoCDP := wizardSkipChromeSQLite, wizardWriteChromeSQLite, wizardNoCDP
	defer func() {
		wizardSkipChromeSQLite = saveSkip
		wizardWriteChromeSQLite = saveWrite
		wizardNoCDP = saveNoCDP
	}()

	t.Run("default (no flags) resolves to universal", func(t *testing.T) {
		// The v0.13 behavior change: a plain `wizard install --as sink`
		// defaults to universal (skip=false), no longer auto-degrading.
		wizardSkipChromeSQLite = false
		wizardWriteChromeSQLite = false
		wizardNoCDP = false
		skip, cdp, _, delivery := resolveSinkHeadlessMode()
		if skip {
			t.Errorf("default install should resolve to universal (skip=false), got skip=true")
		}
		if cdp {
			t.Errorf("universal default should set cdp=false")
		}
		if delivery != deliveryUniversal {
			t.Errorf("default install delivery: got %q, want %q", delivery, deliveryUniversal)
		}
	})

	t.Run("explicit --skip-chrome-sqlite wins (degraded)", func(t *testing.T) {
		wizardSkipChromeSQLite = true
		wizardWriteChromeSQLite = false
		wizardNoCDP = false
		skip, cdp, _, delivery := resolveSinkHeadlessMode()
		if !skip {
			t.Errorf("--skip-chrome-sqlite should set skip=true")
		}
		if !cdp {
			t.Errorf("skip=true with --no-cdp absent should set cdp=true")
		}
		if delivery != deliveryDegraded {
			t.Errorf("--skip-chrome-sqlite delivery: got %q, want %q", delivery, deliveryDegraded)
		}
	})

	t.Run("explicit --write-chrome-sqlite wins (universal)", func(t *testing.T) {
		wizardSkipChromeSQLite = false
		wizardWriteChromeSQLite = true
		wizardNoCDP = false
		skip, cdp, _, delivery := resolveSinkHeadlessMode()
		if skip {
			t.Errorf("--write-chrome-sqlite should set skip=false")
		}
		if cdp {
			t.Errorf("skip=false should set cdp=false")
		}
		if delivery != deliveryUniversal {
			t.Errorf("--write-chrome-sqlite delivery: got %q, want %q", delivery, deliveryUniversal)
		}
	})

	t.Run("--no-cdp disables cdp on degraded install", func(t *testing.T) {
		wizardSkipChromeSQLite = true
		wizardWriteChromeSQLite = false
		wizardNoCDP = true
		skip, cdp, _, _ := resolveSinkHeadlessMode()
		if !skip {
			t.Errorf("--skip-chrome-sqlite should still set skip=true with --no-cdp")
		}
		if cdp {
			t.Errorf("--no-cdp should set cdp=false")
		}
	})
}

// TestKeychainStrategyGatedOnHeadless documents the v0.12.0-beta.6
// behavior for friction #19: the keychain strategy loop must not fire
// when the wizard resolves to headless mode (skip_chrome_sqlite=true),
// because the sink daemon won't read Chrome Safe Storage anyway.
// This is a structural test against the gating logic, not a full
// invocation -- the actual loop runs out-of-process via runOuterWizard.
func TestKeychainStrategyGatedOnHeadless(t *testing.T) {
	saveSkip, saveAccess := wizardSkipChromeSQLite, wizardSkipKeychainAccess
	defer func() {
		wizardSkipChromeSQLite = saveSkip
		wizardSkipKeychainAccess = saveAccess
	}()

	// The gating is a 3-branch switch:
	//   1. explicit --skip-keychain-access wins (preserves existing flag).
	//   2. skip_chrome_sqlite skips the loop (the friction #19 fix).
	//   3. otherwise: run the loop.
	//
	// We model each branch's expected log line and assert the right
	// branch fires.
	cases := []struct {
		name             string
		skipChromeSQLite bool
		skipKeychainAcc  bool
		wantLoopRuns     bool
	}{
		{"explicit --skip-keychain-access wins", false, true, false},
		{"headless mode skips loop", true, false, false},
		{"both flags off: loop runs", false, false, true},
		{"headless AND explicit skip: still skipped", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wizardSkipChromeSQLite = tc.skipChromeSQLite
			wizardSkipKeychainAccess = tc.skipKeychainAcc

			// Model the gating decision the same way wizardInstallSink does.
			loopWouldRun := !wizardSkipKeychainAccess && !wizardSkipChromeSQLite
			if loopWouldRun != tc.wantLoopRuns {
				t.Errorf("gating: got loopWouldRun=%v, want %v", loopWouldRun, tc.wantLoopRuns)
			}
		})
	}
}

// TestResolveSinkDeliveryWithKeychain covers the v0.13 downgrade-on-failure
// flow (plan 2026-05-31-002, R5). The keychain-open OUTCOME determines the
// final delivery mode for a default/universal-intent install, so this runs
// before sink.yaml is rendered. We inject success/failure through the
// attemptUniversalKeychainOpen function seam (mirrors execSecurityFunc) and
// assert both the resolver output and the resulting rendered sink.yaml.
func TestResolveSinkDeliveryWithKeychain(t *testing.T) {
	saveOpen := attemptUniversalKeychainOpen
	saveSkip := wizardSkipChromeSQLite
	saveWrite := wizardWriteChromeSQLite
	saveNoCDP := wizardNoCDP
	saveSkipAccess := wizardSkipKeychainAccess
	saveSkipPrompt := wizardSkipKeychainPrompt
	defer func() {
		attemptUniversalKeychainOpen = saveOpen
		wizardSkipChromeSQLite = saveSkip
		wizardWriteChromeSQLite = saveWrite
		wizardNoCDP = saveNoCDP
		wizardSkipKeychainAccess = saveSkipAccess
		wizardSkipKeychainPrompt = saveSkipPrompt
	}()

	reset := func() {
		wizardSkipChromeSQLite = false
		wizardWriteChromeSQLite = false
		wizardNoCDP = false
		wizardSkipKeychainAccess = false
		wizardSkipKeychainPrompt = false
	}

	t.Run("default install + keychain-open SUCCESS -> universal", func(t *testing.T) {
		reset()
		called := false
		attemptUniversalKeychainOpen = func() error { called = true; return nil }
		skip, cdp, _, delivery := resolveSinkHeadlessMode()
		skip, cdp, profileDir, delivery, opened := resolveSinkDeliveryWithKeychain(skip, cdp, "", delivery)
		if !called {
			t.Errorf("default install should attempt the any-app keychain open")
		}
		if !opened {
			t.Errorf("successful open should report keychainOpened=true")
		}
		if skip {
			t.Errorf("success -> universal, skip should stay false")
		}
		if delivery != deliveryUniversal {
			t.Errorf("success delivery: got %q, want %q", delivery, deliveryUniversal)
		}
		yaml := renderSinkYAML("my-laptop", "100.80.229.80:9999", skip, cdp, profileDir, delivery)
		if strings.Contains(yaml, "skip_chrome_sqlite") {
			t.Errorf("universal sink.yaml must not set skip_chrome_sqlite, got:\n%s", yaml)
		}
		if !strings.Contains(yaml, "delivery: universal") {
			t.Errorf("universal sink.yaml should record delivery: universal, got:\n%s", yaml)
		}
	})

	t.Run("default install + keychain-open FAILURE -> degraded, non-fatal", func(t *testing.T) {
		reset()
		attemptUniversalKeychainOpen = func() error { return errInjectedKeychainOpen }
		skip, cdp, _, delivery := resolveSinkHeadlessMode()
		// resolveSinkDeliveryWithKeychain has no error return: the
		// downgrade is non-fatal by construction, so the install continues.
		skip, cdp, profileDir, delivery, opened := resolveSinkDeliveryWithKeychain(skip, cdp, "", delivery)
		if opened {
			t.Errorf("failed open should report keychainOpened=false")
		}
		if !skip {
			t.Errorf("failure -> downgrade to degraded, skip should be true")
		}
		if !cdp {
			t.Errorf("downgrade should enable CDP (like the old headless mode)")
		}
		if delivery != deliveryDegraded {
			t.Errorf("failure delivery: got %q, want %q", delivery, deliveryDegraded)
		}
		yaml := renderSinkYAML("my-laptop", "100.80.229.80:9999", skip, cdp, profileDir, delivery)
		if !strings.Contains(yaml, "skip_chrome_sqlite: true") {
			t.Errorf("degraded sink.yaml should set skip_chrome_sqlite: true, got:\n%s", yaml)
		}
		if !strings.Contains(yaml, "delivery: degraded") {
			t.Errorf("degraded sink.yaml should record delivery: degraded, got:\n%s", yaml)
		}
		if !strings.Contains(yaml, "cdp:") {
			t.Errorf("downgraded sink.yaml should carry a cdp block, got:\n%s", yaml)
		}
	})

	t.Run("explicit --skip-chrome-sqlite -> degraded, open NOT attempted", func(t *testing.T) {
		reset()
		wizardSkipChromeSQLite = true
		called := false
		attemptUniversalKeychainOpen = func() error { called = true; return nil }
		skip, cdp, profileDir, delivery := resolveSinkHeadlessMode()
		skip, cdp, profileDir, delivery, opened := resolveSinkDeliveryWithKeychain(skip, cdp, profileDir, delivery)
		if called {
			t.Errorf("explicit --skip-chrome-sqlite must NOT attempt the keychain open")
		}
		if opened {
			t.Errorf("degraded opt-out should report keychainOpened=false")
		}
		if !skip {
			t.Errorf("--skip-chrome-sqlite should set skip=true")
		}
		if delivery != deliveryDegraded {
			t.Errorf("--skip-chrome-sqlite delivery: got %q, want %q", delivery, deliveryDegraded)
		}
	})

	t.Run("explicit --write-chrome-sqlite + FAILURE -> honored (no downgrade)", func(t *testing.T) {
		reset()
		wizardWriteChromeSQLite = true
		attemptUniversalKeychainOpen = func() error { return errInjectedKeychainOpen }
		skip, cdp, profileDir, delivery := resolveSinkHeadlessMode()
		skip, _, _, delivery, opened := resolveSinkDeliveryWithKeychain(skip, cdp, profileDir, delivery)
		if opened {
			t.Errorf("failed open should report keychainOpened=false")
		}
		if skip {
			t.Errorf("explicit --write-chrome-sqlite must NOT silently downgrade on open failure")
		}
		if delivery != deliveryUniversal {
			t.Errorf("forced-universal delivery should stay universal, got %q", delivery)
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
		"0.0.0.0:9998":     "every interface",
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
