package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
)

// stubWirer is a controllable Wirer for doctor-check tests. launcherPath
// drives IsWired (which stats it): point it at a real file to simulate a
// wired target, or a nonexistent path to simulate unwired.
type stubWirer struct {
	name         string
	installed    bool
	launcherPath string
}

func (s stubWirer) Name() string                                   { return s.name }
func (s stubWirer) IsInstalled() bool                              { return s.installed }
func (s stubWirer) LauncherPath() string                           { return s.launcherPath }
func (s stubWirer) LaunchSnippet(agentbrowser.AttachTarget) string { return s.name + " --cdp 9222" }
func (s stubWirer) Wire(agentbrowser.AttachTarget) (agentbrowser.WireResult, error) {
	return agentbrowser.WireResult{}, nil
}

func TestCheckAgentBrowserAttach_NoneInstalled(t *testing.T) {
	c := checkAgentBrowserAttachWith(agentattach.Discovery{Reachable: true}, []agentbrowser.Wirer{
		stubWirer{name: "browser-use", installed: false},
	})
	if c.Severity != SeveritySkipped {
		t.Errorf("severity = %v, want skipped", c.Severity)
	}
}

func TestCheckAgentBrowserAttach_Unreachable(t *testing.T) {
	d := agentattach.Discovery{Reachable: false, Tier: agentattach.TierAutoConnect, Remediation: "enable chrome://inspect"}
	c := checkAgentBrowserAttachWith(d, []agentbrowser.Wirer{
		stubWirer{name: "browser-use", installed: true},
	})
	if c.Severity != SeverityWarn {
		t.Errorf("severity = %v, want warn", c.Severity)
	}
	if !strings.Contains(c.Remediation, "chrome://inspect") {
		t.Errorf("remediation = %q", c.Remediation)
	}
}

func TestCheckAgentBrowserAttach_ReachableNotWired(t *testing.T) {
	c := checkAgentBrowserAttachWith(agentattach.Discovery{Reachable: true}, []agentbrowser.Wirer{
		stubWirer{name: "browser-use", installed: true, launcherPath: "/nonexistent/launcher"},
	})
	if c.Severity != SeverityWarn || !strings.Contains(c.Remediation, "--wire") {
		t.Errorf("want warn + --wire remediation, got %v / %q", c.Severity, c.Remediation)
	}
}

func TestCheckAgentBrowserAttach_ReachableWired(t *testing.T) {
	// A real launcher file on disk makes IsWired true -> SeverityOK.
	dir := t.TempDir()
	lp := dir + "/browser-use-attached"
	if err := os.WriteFile(lp, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := checkAgentBrowserAttachWith(agentattach.Discovery{Reachable: true}, []agentbrowser.Wirer{
		stubWirer{name: "browser-use", installed: true, launcherPath: lp},
	})
	if c.Severity != SeverityOK {
		t.Errorf("severity = %v, want ok", c.Severity)
	}
	if c.Remediation != "" {
		t.Errorf("fully-wired check should have no remediation, got %q", c.Remediation)
	}
}

func TestCheckAgentBrowserAttach_PartiallyWired(t *testing.T) {
	dir := t.TempDir()
	lp := dir + "/browser-use-attached"
	if err := os.WriteFile(lp, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := checkAgentBrowserAttachWith(agentattach.Discovery{Reachable: true}, []agentbrowser.Wirer{
		stubWirer{name: "browser-use", installed: true, launcherPath: lp},
		stubWirer{name: "agent-browser", installed: true, launcherPath: dir + "/nonexistent"},
	})
	if c.Severity != SeverityOK || c.Remediation == "" {
		t.Errorf("partial wiring should be OK with a remediation, got %v / %q", c.Severity, c.Remediation)
	}
}
