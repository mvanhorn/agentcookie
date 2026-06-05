package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
)

func TestResolveAttachAction(t *testing.T) {
	cases := []struct {
		print, wire, check bool
		want               string
		wantErr            bool
	}{
		{false, false, false, "wire", false}, // default
		{true, false, false, "print", false},
		{false, true, false, "wire", false},
		{false, false, true, "check", false},
		{true, true, false, "", true}, // conflict
		{true, false, true, "", true}, // conflict
	}
	for _, c := range cases {
		got, err := resolveAttachAction(c.print, c.wire, c.check)
		if (err != nil) != c.wantErr {
			t.Errorf("resolveAttachAction(%v,%v,%v) err=%v wantErr=%v", c.print, c.wire, c.check, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("resolveAttachAction(%v,%v,%v) = %q, want %q", c.print, c.wire, c.check, got, c.want)
		}
	}
}

func TestSelectWirers(t *testing.T) {
	all, err := selectWirers("all")
	if err != nil || len(all) < 2 {
		t.Errorf("selectWirers(all) = %d wirers, err=%v", len(all), err)
	}
	one, err := selectWirers("browser-use")
	if err != nil || len(one) != 1 || one[0].Name() != "browser-use" {
		t.Errorf("selectWirers(browser-use) = %v, err=%v", one, err)
	}
	if _, err := selectWirers("nope"); err == nil {
		t.Error("unknown target should error")
	}
	empty, err := selectWirers("")
	if err != nil || len(empty) < 2 {
		t.Errorf("empty target should default to all, got %d err=%v", len(empty), err)
	}
}

func TestCheckAttach_UnreachableReturnsError(t *testing.T) {
	d := agentattach.Discovery{Reachable: false, Tier: agentattach.TierCustomDirOnly, Remediation: "use --fallback"}
	var buf bytes.Buffer
	err := checkAttach(&buf, d, agentbrowser.All(), false)
	if err == nil {
		t.Error("check should return error when unreachable")
	}
	if !strings.Contains(buf.String(), "use --fallback") {
		t.Errorf("check output missing remediation:\n%s", buf.String())
	}
}

func TestWireAttach_OldChromeRefusesToWire(t *testing.T) {
	// Unreachable + custom-dir-only: the real profile can't be attached on
	// this port, so wiring must refuse and point at the fallback.
	d := agentattach.Discovery{Reachable: false, Tier: agentattach.TierCustomDirOnly, Remediation: "use `agentcookie attach --fallback`"}
	var buf bytes.Buffer
	err := wireAttach(&buf, d, agentbrowser.All(), agentbrowser.AttachTarget{Port: 9222}, false)
	if err == nil {
		t.Error("wire should refuse on custom-dir-only unreachable Chrome")
	}
	if !strings.Contains(buf.String(), "--fallback") {
		t.Errorf("wire output should point to fallback:\n%s", buf.String())
	}
}

func TestPrintAttach_NoMutation(t *testing.T) {
	d := agentattach.Discovery{Reachable: true, Version: 148, Tier: agentattach.TierAutoConnect}
	var buf bytes.Buffer
	if err := printAttach(nil, &buf, d, agentbrowser.All(), agentbrowser.AttachTarget{Port: 9222}, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "reachable") {
		t.Errorf("print should show reachable status:\n%s", out)
	}
	// At least one snippet line present for an installed-or-not target.
	if !strings.Contains(out, "browser-use") {
		t.Errorf("print should list browser-use:\n%s", out)
	}
}
