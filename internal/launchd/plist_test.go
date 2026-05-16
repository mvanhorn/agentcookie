package launchd

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestSpecRenderSourceRole(t *testing.T) {
	s := Spec{
		Role:       RoleSource,
		BinaryPath: "/Users/test/bin/agentcookie",
		LogDir:     "/Users/test/.agentcookie/logs",
		ExtraArgs:  []string{"--watch"},
	}
	out, err := s.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	str := string(out)
	for _, want := range []string{
		"dev.agentcookie.source",
		"/Users/test/bin/agentcookie",
		"<string>source</string>",
		"<string>--watch</string>",
		"/Users/test/.agentcookie/logs/source.out.log",
		"/Users/test/.agentcookie/logs/source.err.log",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>ProcessType</key>",
		"<string>Background</string>",
	} {
		if !strings.Contains(str, want) {
			t.Errorf("rendered plist missing %q\nFull output:\n%s", want, str)
		}
	}
}

func TestSpecRenderSinkRole(t *testing.T) {
	s := Spec{
		Role:       RoleSink,
		BinaryPath: "/Users/test/bin/agentcookie",
		LogDir:     "/Users/test/.agentcookie/logs",
	}
	out, err := s.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	str := string(out)
	if !strings.Contains(str, "dev.agentcookie.sink") {
		t.Errorf("rendered plist missing sink label")
	}
	if strings.Contains(str, "--watch") {
		t.Errorf("sink plist should not contain --watch arg by default")
	}
}

func TestSpecRenderProducesValidXML(t *testing.T) {
	s := Spec{
		Role:       RoleSource,
		BinaryPath: "/Users/test/bin/agentcookie",
		LogDir:     "/Users/test/.agentcookie/logs",
		ExtraArgs:  []string{"--watch"},
	}
	out, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	// Parse as XML; if the template is malformed it errors here.
	var parsed any
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Errorf("rendered plist failed XML parse: %v", err)
	}
}

func TestSpecRenderRequiresBinaryPath(t *testing.T) {
	s := Spec{Role: RoleSource, LogDir: "/tmp/logs"}
	if _, err := s.Render(); err == nil {
		t.Error("expected error for missing BinaryPath")
	}
}

func TestSpecRenderRequiresLogDir(t *testing.T) {
	s := Spec{Role: RoleSource, BinaryPath: "/x"}
	if _, err := s.Render(); err == nil {
		t.Error("expected error for missing LogDir")
	}
}

func TestSpecRenderRejectsInvalidRole(t *testing.T) {
	s := Spec{Role: Role("bogus"), BinaryPath: "/x", LogDir: "/y"}
	if _, err := s.Render(); err == nil {
		t.Error("expected error for invalid Role")
	}
}

func TestSpecLabelIsRoleBased(t *testing.T) {
	cases := map[Role]string{
		RoleSource: "dev.agentcookie.source",
		RoleSink:   "dev.agentcookie.sink",
	}
	for role, want := range cases {
		got := Spec{Role: role}.Label()
		if got != want {
			t.Errorf("Label for %s = %q, want %q", role, got, want)
		}
	}
}
