package agentbrowser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentBrowser_Wire_WritesPortLauncher(t *testing.T) {
	dir := withTempDir(t)
	a := newAgentBrowserAt("/opt/homebrew/bin/agent-browser")

	res, err := a.Wire(AttachTarget{Port: 9222})
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	want := filepath.Join(dir, "agent-browser-attached")
	if res.LauncherPath != want {
		t.Errorf("LauncherPath = %q, want %q", res.LauncherPath, want)
	}
	body, _ := os.ReadFile(want)
	s := string(body)
	if !strings.Contains(s, "--cdp") || !strings.Contains(s, "9222") {
		t.Errorf("launcher missing --cdp <port>:\n%s", s)
	}
	if !strings.Contains(s, "'/opt/homebrew/bin/agent-browser'") {
		t.Errorf("launcher does not exec the real binary:\n%s", s)
	}
}

func TestAgentBrowser_Wire_RequiresPort(t *testing.T) {
	withTempDir(t)
	a := newAgentBrowserAt("/opt/homebrew/bin/agent-browser")
	if _, err := a.Wire(AttachTarget{WSEndpoint: "ws://127.0.0.1:9222/x"}); err == nil {
		t.Error("agent-browser Wire without a port should fail")
	}
}

func TestAgentBrowser_Wire_Idempotent(t *testing.T) {
	withTempDir(t)
	a := newAgentBrowserAt("/opt/homebrew/bin/agent-browser")
	r1, _ := a.Wire(AttachTarget{Port: 9222})
	first, _ := os.ReadFile(r1.LauncherPath)
	r2, _ := a.Wire(AttachTarget{Port: 9222})
	second, _ := os.ReadFile(r2.LauncherPath)
	if string(first) != string(second) {
		t.Error("re-wiring same port changed bytes")
	}
}

func TestAgentBrowser_LaunchSnippet(t *testing.T) {
	a := newAgentBrowserAt("/opt/homebrew/bin/agent-browser")
	snip := a.LaunchSnippet(AttachTarget{Port: 9222})
	if !strings.Contains(snip, "--cdp 9222") {
		t.Errorf("snippet missing --cdp: %q", snip)
	}
	if !strings.Contains(snip, "--auto-connect") {
		t.Errorf("snippet should mention --auto-connect: %q", snip)
	}
	// Port-less target falls back to auto-connect.
	if got := a.LaunchSnippet(AttachTarget{}); !strings.Contains(got, "--auto-connect") {
		t.Errorf("port-less snippet = %q, want --auto-connect", got)
	}
}

func TestAgentBrowser_Registered(t *testing.T) {
	if _, ok := Lookup("agent-browser"); !ok {
		t.Error("agent-browser should be registered in All()")
	}
}
