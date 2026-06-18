package cli

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// TestCheckBinarySignature covers the three branches the binary
// identity check can produce: Developer-ID signed (OK), ad-hoc local
// build (WARN), and a no-codesign environment (also WARN, never FAIL).
func TestCheckBinarySignature(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		err      error
		wantSev  Severity
		wantSubs string
	}{
		{
			name: "developer id signed",
			output: `Executable=/usr/local/bin/agentcookie
designated => identifier "com.mvanhorn.agentcookie" and anchor apple generic and certificate leaf[subject.OU] = "NM8VT393AR"`,
			wantSev:  SeverityOK,
			wantSubs: "NM8VT393AR",
		},
		{
			name:     "ad-hoc signed",
			output:   `Executable=/usr/local/bin/agentcookie\ndesignated => anchor apple generic and certificate leaf[subject.OU] = "OTHER"`,
			wantSev:  SeverityWarn,
			wantSubs: "ad-hoc",
		},
		{
			name:     "codesign missing",
			output:   "",
			err:      errors.New("codesign not found"),
			wantSev:  SeverityWarn,
			wantSubs: "codesign",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := checkBinarySignatureWith(func() (string, error) { return tc.output, tc.err })
			if c.Severity != tc.wantSev {
				t.Errorf("severity: got %q, want %q (detail=%q)", c.Severity, tc.wantSev, c.Detail)
			}
			if tc.wantSubs != "" && !strings.Contains(c.Detail, tc.wantSubs) {
				t.Errorf("detail missing %q: %q", tc.wantSubs, c.Detail)
			}
		})
	}
}

// TestCheckTailscale validates the two outcomes RequireTailnetIP can
// produce as far as doctor cares: an IP (OK) or any error (FAIL with
// a remediation).
func TestCheckTailscale(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		c := checkTailscaleWith(func() (string, error) { return "100.80.229.80", nil })
		if c.Severity != SeverityOK {
			t.Fatalf("got %q, want OK; detail=%q", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "100.80.229.80") {
			t.Errorf("detail missing IP: %q", c.Detail)
		}
	})
	t.Run("daemon down", func(t *testing.T) {
		c := checkTailscaleWith(func() (string, error) { return "", errors.New("daemon down") })
		if c.Severity != SeverityFail {
			t.Fatalf("got %q, want FAIL", c.Severity)
		}
		if !strings.Contains(c.Remediation, "tailscale up") {
			t.Errorf("remediation missing `tailscale up`: %q", c.Remediation)
		}
	})
}

// TestCheckConfig covers the three branches: neither file (FAIL),
// sink-only (OK), source-only (OK).
func TestCheckConfig(t *testing.T) {
	t.Run("neither file", func(t *testing.T) {
		dir := t.TempDir()
		c := checkConfig(dir)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q, want FAIL", c.Severity)
		}
		if !strings.Contains(c.Remediation, "wizard install") {
			t.Errorf("remediation missing wizard install: %q", c.Remediation)
		}
	})
	t.Run("sink only", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "sink.yaml"), `listen:
  addr: 100.80.229.80:9999
peer:
  hostname: macbook-pro
`)
		c := checkConfig(dir)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
	})
	t.Run("source only", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "source.yaml"), `sink:
  url: https://100.80.229.80:9999
peer:
  hostname: mac-mini
`)
		c := checkConfig(dir)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
	})
}

func TestCheckCookiePolicy(t *testing.T) {
	t.Run("missing blocklist is sync all", func(t *testing.T) {
		c := checkCookiePolicy(t.TempDir())
		if c.Severity != SeverityInfo {
			t.Fatalf("got %q, want INFO", c.Severity)
		}
		if !strings.Contains(c.Detail, "sync-all") {
			t.Errorf("detail should report sync-all, got %q", c.Detail)
		}
	})
	t.Run("allowlist with patterns", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "blocklist.yaml"), `version: 1
policy: allowlist
domains:
  - pattern: "example.com"
`)
		c := checkCookiePolicy(dir)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "allowlist") {
			t.Errorf("detail should report allowlist, got %q", c.Detail)
		}
	})
	t.Run("empty allowlist warns", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "blocklist.yaml"), `version: 1
policy: allowlist
domains: []
`)
		c := checkCookiePolicy(dir)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q, want WARN", c.Severity)
		}
		if !strings.Contains(c.Detail, "0 patterns") {
			t.Errorf("detail should report empty allowlist, got %q", c.Detail)
		}
	})
	t.Run("malformed policy fails", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "blocklist.yaml"), `version: 1
policy: denylist
domains: []
`)
		c := checkCookiePolicy(dir)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q, want FAIL", c.Severity)
		}
		if !strings.Contains(c.Detail, "policy") {
			t.Errorf("detail should report policy failure, got %q", c.Detail)
		}
	})
}

// TestCheckKeystore covers paired-key presence + mode 0600 enforcement.
func TestCheckKeystore(t *testing.T) {
	t.Run("key present mode 0600", func(t *testing.T) {
		dir := t.TempDir()
		writeKey(t, dir, "macbook-pro", 0o600)
		c := checkKeystore(dir, []string{"macbook-pro"})
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("key missing", func(t *testing.T) {
		dir := t.TempDir()
		c := checkKeystore(dir, []string{"macbook-pro"})
		if c.Severity != SeverityFail {
			t.Fatalf("got %q", c.Severity)
		}
		if !strings.Contains(c.Remediation, "agentcookie pair") {
			t.Errorf("remediation missing `agentcookie pair`: %q", c.Remediation)
		}
	})
	t.Run("key wrong mode", func(t *testing.T) {
		dir := t.TempDir()
		writeKey(t, dir, "macbook-pro", 0o644)
		c := checkKeystore(dir, []string{"macbook-pro"})
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("no peers (skipped)", func(t *testing.T) {
		dir := t.TempDir()
		c := checkKeystore(dir, nil)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want skipped", c.Severity)
		}
	})
}

// TestCheckSinkListener: if we can bind the address ourselves, the
// sink is NOT listening; FAIL. If bind fails because the port is in
// use, the sink (or something) is listening; OK.
func TestCheckSinkListener(t *testing.T) {
	t.Run("port in use means sink is up", func(t *testing.T) {
		// Bind a port locally so the doctor's competing-bind probe
		// fails -- which is exactly the "sink already listening" path.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		c := checkSinkListener(ln.Addr().String())
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
	})
	t.Run("port free means sink is down", func(t *testing.T) {
		// Pick a free port by binding+immediately-closing.
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		c := checkSinkListener(addr)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q), want FAIL", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Remediation, "launchctl") {
			t.Errorf("remediation missing launchctl: %q", c.Remediation)
		}
	})
}

// TestCheckSinkState covers the three age branches: fresh (OK),
// stale (WARN), missing (FAIL).
func TestCheckSinkState(t *testing.T) {
	t.Run("fresh reports mode", func(t *testing.T) {
		// v0.12.0-beta.3: the detail line surfaces LastWriteMode so a
		// friend reading `doctor` sees whether they're in legacy or
		// headless mode at a glance.
		st := &state.SinkState{
			LastWrite:     time.Now().Add(-5 * time.Minute),
			LastWriteMode: "sidecar+adapter",
		}
		c := checkSinkStateFrom(st, nil)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "mode=sidecar+adapter") {
			t.Errorf("detail should report mode=sidecar+adapter, got %q", c.Detail)
		}
	})
	t.Run("fresh missing mode falls back to unknown", func(t *testing.T) {
		st := &state.SinkState{LastWrite: time.Now().Add(-5 * time.Minute)}
		c := checkSinkStateFrom(st, nil)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "mode=unknown") {
			t.Errorf("missing LastWriteMode should fall back to mode=unknown, got %q", c.Detail)
		}
	})
	t.Run("stale (>24h)", func(t *testing.T) {
		st := &state.SinkState{LastWrite: time.Now().Add(-26 * time.Hour)}
		c := checkSinkStateFrom(st, nil)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
	})
	t.Run("missing", func(t *testing.T) {
		c := checkSinkStateFrom(nil, nil)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
}

// TestCheckSourceState covers fresh+clean (OK), stale (WARN),
// failures>0 (WARN), missing (FAIL).
func TestCheckSourceState(t *testing.T) {
	t.Run("fresh and clean", func(t *testing.T) {
		st := &state.SourceState{LastPush: time.Now().Add(-5 * time.Minute)}
		c := checkSourceStateFrom(st, nil)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("stale", func(t *testing.T) {
		st := &state.SourceState{LastPush: time.Now().Add(-26 * time.Hour)}
		c := checkSourceStateFrom(st, nil)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q", c.Severity)
		}
	})
	t.Run("has failures", func(t *testing.T) {
		st := &state.SourceState{LastPush: time.Now(), TotalFailures: 3}
		c := checkSourceStateFrom(st, nil)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("missing", func(t *testing.T) {
		c := checkSourceStateFrom(nil, nil)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q", c.Severity)
		}
	})
}

func TestCheckDBSC(t *testing.T) {
	t.Run("no state is OK", func(t *testing.T) {
		c := checkDBSCFrom(nil)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("zero suspects is OK", func(t *testing.T) {
		c := checkDBSCFrom(&state.SourceState{LastPush: time.Now()})
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
	})
	t.Run("suspects warn with remediation", func(t *testing.T) {
		st := &state.SourceState{
			LastDBSCWarned: 2,
			LastDBSCSample: []string{"cookie \"SID\" on known DBSC host \".google.com\""},
		}
		c := checkDBSCFrom(st)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
		}
		if c.Remediation == "" {
			t.Fatalf("expected remediation guidance")
		}
	})
}

// TestCheckSealing emits an informational OK either way.
func TestCheckSealing(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		c := checkSealingWith(func() bool { return true })
		if c.Severity != SeverityOK {
			t.Fatalf("got %q", c.Severity)
		}
		if !strings.Contains(c.Detail, "enabled") {
			t.Errorf("detail: %q", c.Detail)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		c := checkSealingWith(func() bool { return false })
		if c.Severity != SeverityOK {
			t.Fatalf("got %q", c.Severity)
		}
		if !strings.Contains(c.Detail, "disabled") {
			t.Errorf("detail: %q", c.Detail)
		}
	})
}

// TestCheckCDPInjector covers the v0.12.0-beta.3 CDP-injection check.
// cdp.enabled=false reports SKIPPED. cdp.enabled=true with a valid
func TestCheckCmuxDelivery(t *testing.T) {
	okProbe := func(string) (string, error) { return "allowAll", nil }
	cmuxOnlyProbe := func(string) (string, error) { return "cmuxOnly", nil }
	errProbe := func(string) (string, error) { return "", errors.New("broken pipe") }

	t.Run("disabled is skipped", func(t *testing.T) {
		cfg := &config.SinkConfig{Cmux: config.CmuxRef{Enabled: false}}
		c := checkCmuxDeliveryWith(cfg.Cmux, "cmux delivery", okProbe)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})

	t.Run("enabled but cmux binary missing warns", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-cmux")
		cfg := &config.SinkConfig{Cmux: config.CmuxRef{Enabled: true, CmuxPath: missing}}
		c := checkCmuxDeliveryWith(cfg.Cmux, "cmux delivery", okProbe)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "not found") {
			t.Errorf("detail: %q", c.Detail)
		}
	})

	t.Run("cmuxOnly mode warns with restart remediation", func(t *testing.T) {
		bin := writeExecutable(t)
		cfg := &config.SinkConfig{Cmux: config.CmuxRef{Enabled: true, CmuxPath: bin}}
		c := checkCmuxDeliveryWith(cfg.Cmux, "cmux delivery", cmuxOnlyProbe)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q, want WARN", c.Severity)
		}
		if !strings.Contains(c.Detail, "cmuxOnly") {
			t.Errorf("detail should name cmuxOnly: %q", c.Detail)
		}
		if !strings.Contains(c.Remediation, "socketControlMode") || !strings.Contains(c.Remediation, "restart") {
			t.Errorf("remediation should mention socketControlMode + restart: %q", c.Remediation)
		}
	})

	t.Run("unreachable socket warns", func(t *testing.T) {
		bin := writeExecutable(t)
		cfg := &config.SinkConfig{Cmux: config.CmuxRef{Enabled: true, CmuxPath: bin}}
		c := checkCmuxDeliveryWith(cfg.Cmux, "cmux delivery", errProbe)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q, want WARN", c.Severity)
		}
	})

	t.Run("reachable non-cmuxOnly mode is OK", func(t *testing.T) {
		bin := writeExecutable(t)
		cfg := &config.SinkConfig{Cmux: config.CmuxRef{Enabled: true, CmuxPath: bin}}
		c := checkCmuxDeliveryWith(cfg.Cmux, "cmux delivery", okProbe)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "allowAll") {
			t.Errorf("detail should report the mode: %q", c.Detail)
		}
	})
}

func writeExecutable(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// profile dir reports OK when Chrome is found. cdp.enabled=true with
// no profile dir reports WARN.
func TestCheckCDPInjector(t *testing.T) {
	t.Run("disabled is skipped", func(t *testing.T) {
		cfg := &config.SinkConfig{CDP: config.CDPRef{Enabled: false}}
		c := checkCDPInjector(cfg)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})
	t.Run("missing profile_dir warns", func(t *testing.T) {
		tmp := t.TempDir()
		missing := filepath.Join(tmp, "no-such-dir")
		cfg := &config.SinkConfig{CDP: config.CDPRef{Enabled: true, ProfileDir: missing}}
		c := checkCDPInjector(cfg)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "profile_dir does not exist") {
			t.Errorf("detail: %q", c.Detail)
		}
	})
	t.Run("present profile_dir reports OK or chrome WARN", func(t *testing.T) {
		// We exercise the path-exists code branch. Whether Chrome is
		// found on the test runner is environment-dependent (CI without
		// Chrome installed will get WARN with a chrome-not-found
		// remediation). Both severities are valid for this test; what
		// we're asserting is that we don't FAIL out.
		tmp := t.TempDir()
		cfg := &config.SinkConfig{CDP: config.CDPRef{Enabled: true, ProfileDir: tmp}}
		c := checkCDPInjector(cfg)
		if c.Severity != SeverityOK && c.Severity != SeverityWarn {
			t.Fatalf("got %q, want OK or WARN", c.Severity)
		}
	})
}

// TestCheckCookieDelivery covers the universal-cookie-delivery check's
// branches (U4): source-only skip, universal OK, degraded INFO, genuinely-
// ungranted partial WARN, locked-SSH false-negative INFO, and the
// duplicate-item race WARN. Uses injected probe + item counter so the test
// never touches the host Keychain.
func TestCheckCookieDelivery(t *testing.T) {
	okProbe := func() (int, error) { return 24, nil }
	ungrantedProbe := func() (int, error) { return 0, errors.New("not readable by this process") }
	lockedProbe := func() (int, error) {
		return 0, errors.New("keybase keychain GetGenericPassword: User interaction is not allowed. (-25308)")
	}
	oneItem := func() (int, error) { return 1, nil }
	twoItems := func() (int, error) { return 2, nil }

	t.Run("source-only skipped", func(t *testing.T) {
		c := checkCookieDeliveryWith(nil, okProbe, oneItem)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})

	t.Run("universal OK when profile written, key readable, one item", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: false, Delivery: "universal"}
		c := checkCookieDeliveryWith(cfg, okProbe, oneItem)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "universal") {
			t.Errorf("detail should mention universal: %q", c.Detail)
		}
	})

	t.Run("universal OK without delivery marker still reads probe as truth", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: false} // Delivery unset
		c := checkCookieDeliveryWith(cfg, okProbe, oneItem)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "any unmodified cookie CLI works here") {
			t.Errorf("detail: %q", c.Detail)
		}
	})

	t.Run("degraded is INFO and names the one-password grant, not --any-app", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: true, Delivery: "degraded"}
		c := checkCookieDeliveryWith(cfg, okProbe, oneItem)
		if c.Severity != SeverityInfo {
			t.Fatalf("got %q (%q), want INFO", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "degraded") {
			t.Errorf("detail should mention degraded: %q", c.Detail)
		}
		if !strings.Contains(c.Remediation, "set-keychain-access") {
			t.Errorf("remediation missing set-keychain-access: %q", c.Remediation)
		}
		if strings.Contains(c.Remediation, "--any-app") {
			t.Errorf("remediation must not advise the obsolete --any-app: %q", c.Remediation)
		}
	})

	t.Run("genuinely ungranted (not locked, one item) warns partial with one-password fix", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: false, Delivery: "universal"}
		c := checkCookieDeliveryWith(cfg, ungrantedProbe, oneItem)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "partial") {
			t.Errorf("detail should mention partial: %q", c.Detail)
		}
		if !strings.Contains(c.Remediation, "set-keychain-access") || strings.Contains(c.Remediation, "--any-app") {
			t.Errorf("remediation should be the one-password grant, not --any-app: %q", c.Remediation)
		}
	})

	t.Run("locked SSH keychain is an INFO false-negative, no destructive fix", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: false, Delivery: "universal"}
		c := checkCookieDeliveryWith(cfg, lockedProbe, oneItem)
		if c.Severity != SeverityInfo {
			t.Fatalf("got %q (%q), want INFO (locked-SSH is not a failure)", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "locked") {
			t.Errorf("detail should explain the locked keychain: %q", c.Detail)
		}
		if strings.Contains(c.Remediation, "--any-app") {
			t.Errorf("locked-SSH must not advise --any-app: %q", c.Remediation)
		}
	})

	t.Run("duplicate-item race warns regardless of probe", func(t *testing.T) {
		cfg := &config.SinkConfig{SkipChromeSQLite: false, Delivery: "universal"}
		// Even with a readable probe, >1 item is the race signature and wins.
		c := checkCookieDeliveryWith(cfg, okProbe, twoItems)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "race") || !strings.Contains(c.Detail, "2") {
			t.Errorf("detail should name the race and item count: %q", c.Detail)
		}
		if !strings.Contains(c.Remediation, "converge") {
			t.Errorf("remediation should mention converge: %q", c.Remediation)
		}
	})
}

func TestCheckSourceAdapter(t *testing.T) {
	exists := func(string) error { return nil }
	password := func(chrome.Browser) (string, error) { return "safe-storage-password", nil }
	decryptOK := func(string, []byte) error { return nil }

	t.Run("sink-only skipped", func(t *testing.T) {
		c := checkSourceAdapter(nil, exists, password, decryptOK)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})

	t.Run("ok reports adapter", func(t *testing.T) {
		cfg := &config.SourceConfig{
			Chrome:  config.ChromeRef{DBPath: "/tmp/Cookies"},
			Browser: config.BrowserRef{Name: "atlas"},
		}
		c := checkSourceAdapter(cfg, exists, password, decryptOK)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q), want OK", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "adapter: atlas") {
			t.Errorf("detail should name atlas adapter: %q", c.Detail)
		}
	})

	t.Run("missing cookies path fails loud", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: "/tmp/missing"}}
		c := checkSourceAdapter(cfg, func(string) error { return os.ErrNotExist }, password, decryptOK)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q), want FAIL", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "cookies SQLite missing") {
			t.Errorf("detail: %q", c.Detail)
		}
	})

	t.Run("keychain unreadable fails loud", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: "/tmp/Cookies"}}
		c := checkSourceAdapter(cfg, exists, func(chrome.Browser) (string, error) {
			return "", errors.New("not allowed")
		}, decryptOK)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q), want FAIL", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "Safe Storage keychain entry unreadable") {
			t.Errorf("detail: %q", c.Detail)
		}
	})

	t.Run("decrypt failure uses requested unsupported shape", func(t *testing.T) {
		cfg := &config.SourceConfig{
			Chrome:  config.ChromeRef{DBPath: "/tmp/Cookies"},
			Browser: config.BrowserRef{Name: "atlas"},
		}
		c := checkSourceAdapter(cfg, exists, password, func(string, []byte) error {
			return errors.New("bad prefix")
		})
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q), want FAIL", c.Severity, c.Detail)
		}
		if c.Detail != "adapter: atlas - decryption: unsupported on this build" {
			t.Errorf("detail: got %q", c.Detail)
		}
	})

	t.Run("no encrypted cookies warns", func(t *testing.T) {
		cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: "/tmp/Cookies"}}
		c := checkSourceAdapter(cfg, exists, password, func(string, []byte) error {
			return errSourceAdapterNoEncryptedCookies
		})
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q (%q), want WARN", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "no encrypted cookies") {
			t.Errorf("detail: %q", c.Detail)
		}
	})

	t.Run("unknown browser lists supported names", func(t *testing.T) {
		cfg := &config.SourceConfig{
			Chrome:  config.ChromeRef{DBPath: "/tmp/Cookies"},
			Browser: config.BrowserRef{Name: "dia"},
		}
		c := checkSourceAdapter(cfg, exists, password, decryptOK)
		if c.Severity != SeverityFail {
			t.Fatalf("got %q (%q), want FAIL", c.Severity, c.Detail)
		}
		if !strings.Contains(c.Detail, "supported:") || !strings.Contains(c.Detail, "chrome") {
			t.Errorf("detail should list supported names: %q", c.Detail)
		}
	})
}

// TestHostMatchesAnyAdapter covers the substring fallback used by the
// adapter coverage check.
func TestHostMatchesAnyAdapter(t *testing.T) {
	adapters := []sinkpush.Adapter{&stubAdapter{patterns: []string{"%instacart.com", "%airbnb.com"}}}
	if !hostMatchesAnyAdapter(".instacart.com", adapters) {
		t.Errorf("expected match for .instacart.com")
	}
	if hostMatchesAnyAdapter(".example.com", adapters) {
		t.Errorf("did not expect match for .example.com")
	}
	// Empty pattern shouldn't crash; treated as non-matching.
	emptyAdapter := []sinkpush.Adapter{&stubAdapter{patterns: []string{"%"}}}
	if hostMatchesAnyAdapter("anything", emptyAdapter) {
		t.Errorf("bare %% pattern should not match (the stripped form is empty)")
	}
}

type stubAdapter struct {
	patterns []string
}

func (s *stubAdapter) Name() string                 { return "stub" }
func (s *stubAdapter) CLIBinary() string            { return "/usr/local/bin/stub-pp-cli" }
func (s *stubAdapter) IsInstalled() bool            { return true }
func (s *stubAdapter) CookieHostPatterns() []string { return s.patterns }
func (s *stubAdapter) Push(_ []chrome.Cookie) error { return nil }

// TestRunDoctorJSONEnvelope confirms --json emits a stable envelope
// with all eight checks present (skipped for the wrong role).
func TestRunDoctorJSONEnvelope(t *testing.T) {
	dir := t.TempDir()
	// Set up a source-only install: source.yaml, paired key, and
	// a source-state.json under home/.agentcookie/.
	writeFile(t, filepath.Join(dir, "source.yaml"), `sink:
  url: https://100.80.229.80:9999
peer:
  hostname: mac-mini
`)
	writeKey(t, dir, "mac-mini", 0o600)

	report := buildReport(doctorDeps{
		ConfigDir: dir,
		BinarySignature: func() (string, error) {
			return "designated => anchor apple generic and certificate leaf[subject.OU] = \"NM8VT393AR\"", nil
		},
		TailscaleIP: func() (string, error) { return "100.80.229.80", nil },
		LoadSourceState: func() (*state.SourceState, error) {
			return &state.SourceState{LastPush: time.Now().Add(-30 * time.Second)}, nil
		},
		LoadSinkState:              func() (*state.SinkState, error) { return nil, nil },
		MasterKeyExists:            func() bool { return false },
		SourceAdapterCookiesExists: func(string) error { return nil },
		SourceAdapterPassword:      func(chrome.Browser) (string, error) { return "safe-storage-password", nil },
		SourceAdapterDecrypt:       func(string, []byte) error { return nil },
		// Stub so the envelope test never opens a live cmux browser surface.
		CmuxSessionHealth: func(config.CmuxRef) Check {
			return Check{Name: "cmux session health", Severity: SeveritySkipped, Detail: "stub"}
		},
	})

	// v0.12.0-beta.3 added two checks: Adapter coverage + CDP injector.
	// v0.13 added the Secrets bus check. DBSC resilience added the DBSC
	// check (source role only; present here since this fixture is source).
	// The consumption bridge added the Secret coverage + Binary install checks.
	// Universal cookie delivery added the Cookie delivery check. Source browser
	// adapters added the Source adapter check. The cmux delivery surface added
	// the cmux delivery check. Browser-bound-session honesty added the cmux
	// session health check (source role only; present here since this fixture
	// is source).
	if got := len(report.Checks); got != 20 {
		t.Fatalf("got %d checks, want 20", got)
	}

	// Serialize the envelope and confirm it round-trips.
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back DoctorReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ExitCode != report.ExitCode {
		t.Errorf("exit code round-trip drifted: %d vs %d", back.ExitCode, report.ExitCode)
	}

	// Sink-only checks should be Skipped on a source-only install.
	want := map[string]Severity{
		"Sink listener":   SeveritySkipped,
		"Sink state":      SeveritySkipped,
		"Sealing":         SeveritySkipped,
		"Cookie delivery": SeveritySkipped,
		"Source adapter":  SeverityOK,
	}
	for _, c := range report.Checks {
		if w, ok := want[c.Name]; ok && c.Severity != w {
			t.Errorf("%s: got %q, want %q", c.Name, c.Severity, w)
		}
	}
}

// TestRunDoctorExitCodes confirms exit_code maps to FAIL presence.
func TestRunDoctorExitCodes(t *testing.T) {
	dir := t.TempDir()
	// All-fail-ish: no config at all.
	report := buildReport(doctorDeps{
		ConfigDir:       dir,
		BinarySignature: func() (string, error) { return "", errors.New("missing") },
		TailscaleIP:     func() (string, error) { return "", errors.New("daemon down") },
		LoadSourceState: func() (*state.SourceState, error) { return nil, nil },
		LoadSinkState:   func() (*state.SinkState, error) { return nil, nil },
		MasterKeyExists: func() bool { return false },
	})
	if report.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code when checks FAIL; got 0")
	}
}

// --- helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeKey(t *testing.T, configDir, peer string, mode os.FileMode) {
	t.Helper()
	dir := keystore.Dir(configDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, peer+".json")
	if err := os.WriteFile(path, []byte(`{"peer":"`+peer+`","key":"AAAA"}`), mode); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile may not set the requested mode if the file exists with
	// a different one; chmod explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCmuxLocalLoop(t *testing.T) {
	agentUp := func(launchd.Spec) bool { return true }
	agentDown := func(launchd.Spec) bool { return false }
	okProbe := func(string) (string, error) { return "allowAll", nil }
	cmuxOnlyProbe := func(string) (string, error) { return "cmuxOnly", nil }

	t.Run("agent down + cmux absent = skipped (not set up)", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: filepath.Join(t.TempDir(), "no-cmux")}
		c := checkCmuxLocalLoopWith(cfg, agentDown, okProbe)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})

	t.Run("agent down + cmux present = warn run enable", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxLocalLoopWith(cfg, agentDown, okProbe)
		if c.Severity != SeverityWarn || !strings.Contains(c.Remediation, "enable") {
			t.Fatalf("got %q / %q, want WARN + enable hint", c.Severity, c.Remediation)
		}
	})

	t.Run("agent up + still cmuxOnly = warn restart", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxLocalLoopWith(cfg, agentUp, cmuxOnlyProbe)
		if c.Severity != SeverityWarn || !strings.Contains(c.Remediation, "RESTART") {
			t.Fatalf("got %q / %q, want WARN + restart hint", c.Severity, c.Remediation)
		}
	})

	t.Run("agent up + allowAll = OK live", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxLocalLoopWith(cfg, agentUp, okProbe)
		if c.Severity != SeverityOK || !strings.Contains(c.Detail, "loop active") {
			t.Fatalf("got %q / %q, want OK + loop active", c.Severity, c.Detail)
		}
	})
}

func TestCheckCmuxSessionHealth(t *testing.T) {
	okProbe := func(string) (string, error) { return "allowAll", nil }
	downProbe := func(string) (string, error) { return "", errors.New("connection refused") }
	authedVerify := func([]sinkpush.VerifySpec) []sinkpush.VerifyResult {
		return []sinkpush.VerifyResult{{Host: "github.com", State: sinkpush.AuthYes}}
	}
	notAuthedVerify := func([]sinkpush.VerifySpec) []sinkpush.VerifyResult {
		return []sinkpush.VerifyResult{{Host: "github.com", State: sinkpush.AuthNo, Detail: "bound"}}
	}
	unknownVerify := func([]sinkpush.VerifySpec) []sinkpush.VerifyResult {
		return []sinkpush.VerifyResult{{Host: "github.com", State: sinkpush.AuthUnknown, Detail: "timeout"}}
	}
	mustNotVerify := func([]sinkpush.VerifySpec) []sinkpush.VerifyResult {
		t.Helper()
		t.Fatal("verify should not run when cmux is absent or unreachable")
		return nil
	}

	t.Run("cmux absent = skipped, no probe", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: filepath.Join(t.TempDir(), "no-cmux")}
		c := checkCmuxSessionHealthWith(cfg, func(string) (string, error) {
			t.Fatal("probe should not run when cmux is absent")
			return "", nil
		}, mustNotVerify)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED", c.Severity)
		}
	})

	t.Run("cmux not running = skipped, no verify", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxSessionHealthWith(cfg, downProbe, mustNotVerify)
		if c.Severity != SeveritySkipped || !strings.Contains(c.Detail, "not running") {
			t.Fatalf("got %q / %q, want SKIPPED + not running", c.Severity, c.Detail)
		}
	})

	t.Run("authenticated = OK", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxSessionHealthWith(cfg, okProbe, authedVerify)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q / %q, want OK", c.Severity, c.Detail)
		}
	})

	t.Run("not authenticated = WARN with native-login fix", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxSessionHealthWith(cfg, okProbe, notAuthedVerify)
		if c.Severity != SeverityWarn {
			t.Fatalf("got %q, want WARN", c.Severity)
		}
		if !strings.Contains(c.Detail, "github.com") || !strings.Contains(c.Remediation, "log in") {
			t.Fatalf("WARN must name the host and the native-login fix, got %q / %q", c.Detail, c.Remediation)
		}
	})

	t.Run("inconclusive probe = skipped, never FAIL", func(t *testing.T) {
		cfg := config.CmuxRef{CmuxPath: writeExecutable(t)}
		c := checkCmuxSessionHealthWith(cfg, okProbe, unknownVerify)
		if c.Severity != SeveritySkipped {
			t.Fatalf("got %q, want SKIPPED (a flaky probe must never FAIL doctor)", c.Severity)
		}
	})
}

func TestCheckCmuxLocalLoop_ConfigEnabledButAgentDown(t *testing.T) {
	// Greptile: cmux.enabled in config but the launch agent is not running
	// means the loop is not actually syncing -> WARN, not OK.
	agentDown := func(launchd.Spec) bool { return false }
	okProbe := func(string) (string, error) { return "allowAll", nil }
	cfg := config.CmuxRef{Enabled: true, CmuxPath: writeExecutable(t)}
	c := checkCmuxLocalLoopWith(cfg, agentDown, okProbe)
	if c.Severity != SeverityWarn {
		t.Fatalf("config-enabled + agent down should WARN, got %q (%q)", c.Severity, c.Detail)
	}
	if !strings.Contains(c.Remediation, "enable") {
		t.Errorf("remediation should point at enable: %q", c.Remediation)
	}
}
