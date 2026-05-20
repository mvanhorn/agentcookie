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

	"github.com/mvanhorn/agentcookie/internal/keystore"
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
	t.Run("fresh", func(t *testing.T) {
		st := &state.SinkState{LastWrite: time.Now().Add(-5 * time.Minute)}
		c := checkSinkStateFrom(st, nil)
		if c.Severity != SeverityOK {
			t.Fatalf("got %q (%q)", c.Severity, c.Detail)
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
		ConfigDir:        dir,
		BinarySignature:  func() (string, error) { return "designated => anchor apple generic and certificate leaf[subject.OU] = \"NM8VT393AR\"", nil },
		TailscaleIP:      func() (string, error) { return "100.80.229.80", nil },
		LoadSourceState:  func() (*state.SourceState, error) { return &state.SourceState{LastPush: time.Now().Add(-30 * time.Second)}, nil },
		LoadSinkState:    func() (*state.SinkState, error) { return nil, nil },
		MasterKeyExists:  func() bool { return false },
	})

	if got := len(report.Checks); got != 8 {
		t.Fatalf("got %d checks, want 8", got)
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
		"Sink listener": SeveritySkipped,
		"Sink state":    SeveritySkipped,
		"Sealing":       SeveritySkipped,
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
		ConfigDir:        dir,
		BinarySignature:  func() (string, error) { return "", errors.New("missing") },
		TailscaleIP:      func() (string, error) { return "", errors.New("daemon down") },
		LoadSourceState:  func() (*state.SourceState, error) { return nil, nil },
		LoadSinkState:    func() (*state.SinkState, error) { return nil, nil },
		MasterKeyExists:  func() bool { return false },
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
