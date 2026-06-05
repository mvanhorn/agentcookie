package cli

import (
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
)

// stubWirer is a controllable Wirer for doctor-check tests.
type stubWirer struct {
	name      string
	installed bool
	wired     bool
}

func (s stubWirer) Name() string                                   { return s.name }
func (s stubWirer) IsInstalled() bool                              { return s.installed }
func (s stubWirer) BinaryPath() string                             { return "/bin/" + s.name }
func (s stubWirer) LauncherPath() string                           { return "/tmp/agentcookie-test-launcher-" + s.name }
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
		stubWirer{name: "browser-use", installed: true, wired: false},
	})
	if c.Severity != SeverityWarn || !strings.Contains(c.Remediation, "--wire") {
		t.Errorf("want warn + --wire remediation, got %v / %q", c.Severity, c.Remediation)
	}
}
