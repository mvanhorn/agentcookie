package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/macapp"
)

func writeSpecPlist(t *testing.T, home string, s launchd.Spec) {
	t.Helper()
	s.LogDir = filepath.Join(home, ".agentcookie", "logs")
	b, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, s.Label()+".plist"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func installFakeLauncher(t *testing.T, home string) string {
	t.Helper()
	l := macapp.Launcher(home)
	if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestPlistProgram(t *testing.T) {
	bareXML, _ := launchd.Spec{Role: launchd.RoleSource, BinaryPath: "/h/bin/agentcookie", LogDir: "/h/logs"}.Render()
	if l, tgt := plistProgram(bareXML); l != "" || tgt != "/h/bin/agentcookie" {
		t.Errorf("bare: launcher=%q target=%q", l, tgt)
	}
	launcher := "/h/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher"
	wrapped, _ := launchd.Spec{Role: launchd.RoleSource, Launcher: launcher, BinaryPath: "/h/a&b/agentcookie", LogDir: "/h/logs"}.Render()
	if l, tgt := plistProgram(wrapped); l != launcher || tgt != "/h/a&b/agentcookie" {
		t.Errorf("wrapped: launcher=%q target=%q", l, tgt)
	}
}

func TestExtractLaunchAgentBinaryPathSkipsLauncher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, Launcher: macapp.Launcher(home), BinaryPath: "/nonexistent/bin/agentcookie"})
	if got := extractLaunchAgentBinaryPath(launchd.Spec{Role: launchd.RoleSource}); got != "/nonexistent/bin/agentcookie" {
		t.Errorf("extractLaunchAgentBinaryPath = %q, want the target after the launcher", got)
	}
}

func TestCheckLoginItems(t *testing.T) {
	const bin = "/h/bin/agentcookie"
	accept := func(string) error { return nil }
	reject := func(string) error { return errors.New("rejected") }

	cases := []struct {
		name        string
		setup       func(t *testing.T, home string)
		appErr      error
		verify      func(string) error
		severity    Severity
		detail      string
		remediation string
	}{
		{
			name:     "no plists",
			setup:    func(t *testing.T, home string) {},
			verify:   accept,
			severity: SeveritySkipped,
		},
		{
			name: "fully wrapped",
			setup: func(t *testing.T, home string) {
				l := installFakeLauncher(t, home)
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, Launcher: l, BinaryPath: bin})
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleCmuxSync, Launcher: l, BinaryPath: bin})
			},
			verify:   accept,
			severity: SeverityOK,
			detail:   `dev.agentcookie.source, dev.agentcookie.cmux-sync show as "agentcookie"`,
		},
		{
			name: "bare but wrappable",
			setup: func(t *testing.T, home string) {
				installFakeLauncher(t, home)
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, BinaryPath: bin})
			},
			verify:      accept,
			severity:    SeverityWarn,
			detail:      "still shows the signer's name",
			remediation: "agentcookie services refresh",
		},
		{
			name: "no valid app",
			setup: func(t *testing.T, home string) {
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, BinaryPath: bin})
			},
			appErr:      errors.New("not found"),
			verify:      accept,
			severity:    SeverityWarn,
			detail:      "agentcookie.app is not installed",
			remediation: "install a release build",
		},
		{
			name: "reverted",
			setup: func(t *testing.T, home string) {
				installFakeLauncher(t, home)
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleCmuxSync, BinaryPath: bin})
				os.WriteFile(launchd.RevertMarker(home), nil, 0o644)
			},
			verify:      accept,
			severity:    SeverityWarn,
			detail:      "--revert",
			remediation: "--wrap",
		},
		{
			name: "wrapped target rejected",
			setup: func(t *testing.T, home string) {
				l := installFakeLauncher(t, home)
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, Launcher: l, BinaryPath: bin})
			},
			verify:      reject,
			severity:    SeverityWarn,
			detail:      "launcher refuses /h/bin/agentcookie",
			remediation: "agentcookie services refresh",
		},
		{
			name: "wrapped launcher missing",
			setup: func(t *testing.T, home string) {
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, Launcher: macapp.Launcher(home), BinaryPath: bin})
			},
			appErr:      errors.New("not found"),
			verify:      accept,
			severity:    SeverityWarn,
			detail:      "which is missing",
			remediation: "do not delete",
		},
		{
			name: "sink bare is info",
			setup: func(t *testing.T, home string) {
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSink, BinaryPath: bin})
			},
			verify:   accept,
			severity: SeverityInfo,
			detail:   "Automation grant",
		},
		{
			name: "development build is info",
			setup: func(t *testing.T, home string) {
				installFakeLauncher(t, home)
				writeSpecPlist(t, home, launchd.Spec{Role: launchd.RoleSource, BinaryPath: bin})
			},
			verify:   reject,
			severity: SeverityInfo,
			detail:   "not release-signed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			os.MkdirAll(filepath.Join(home, ".agentcookie"), 0o755)
			c.setup(t, home)
			got := checkLoginItems(loginItemsEnv{
				home:         home,
				validateApp:  func(string) error { return c.appErr },
				verifyTarget: c.verify,
			})
			if got.Severity == SeverityFail {
				t.Fatal("login items must never FAIL")
			}
			if got.Severity != c.severity {
				t.Errorf("severity = %s, want %s (%s)", got.Severity, c.severity, got.Detail)
			}
			if !strings.Contains(got.Detail, c.detail) {
				t.Errorf("detail %q missing %q", got.Detail, c.detail)
			}
			if !strings.Contains(got.Remediation, c.remediation) {
				t.Errorf("remediation %q missing %q", got.Remediation, c.remediation)
			}
		})
	}
}
