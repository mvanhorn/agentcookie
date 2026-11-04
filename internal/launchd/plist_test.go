package launchd

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
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

func TestSpecRenderCmuxSyncRole(t *testing.T) {
	s := Spec{
		Role:       RoleCmuxSync,
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
		"dev.agentcookie.cmux-sync",
		"<string>cmux-sync</string>",
		"<string>--watch</string>",
		"/Users/test/.agentcookie/logs/cmux-sync.out.log",
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

// legacyPlistTmpl is the template Render used before Spec.Launcher existed.
// A spec without a launcher must still render exactly this, so upgrading
// agentcookie leaves unwrapped plists byte-for-byte unchanged.
var legacyPlistTmpl = template.Must(template.New("legacy").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>

  <key>ProgramArguments</key>
  <array>
    <string>{{.BinaryPath}}</string>
{{range .Args}}    <string>{{.}}</string>
{{end}}  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
    <key>Crashed</key>
    <true/>
  </dict>

  <key>ThrottleInterval</key>
  <integer>10</integer>

  <key>ProcessType</key>
  <string>Background</string>

  <key>StandardOutPath</key>
  <string>{{.StandardOut}}</string>

  <key>StandardErrorPath</key>
  <string>{{.StandardError}}</string>
</dict>
</plist>
`))

func TestSpecRenderWithoutLauncherIsUnchanged(t *testing.T) {
	for _, s := range []Spec{
		{Role: RoleSource, BinaryPath: "/Users/test/bin/agentcookie", LogDir: "/Users/test/.agentcookie/logs", ExtraArgs: []string{"--watch"}},
		{Role: RoleSink, BinaryPath: "/Users/test/bin/agentcookie", LogDir: "/Users/test/.agentcookie/logs"},
		{Role: RoleCmuxSync, BinaryPath: "/Users/test/go/bin/agentcookie", LogDir: "/Users/test/.agentcookie/logs", ExtraArgs: []string{"--watch"}},
	} {
		got, err := s.Render()
		if err != nil {
			t.Fatal(err)
		}
		var want bytes.Buffer
		if err := legacyPlistTmpl.Execute(&want, plistData{
			Label:         s.Label(),
			BinaryPath:    s.BinaryPath,
			Args:          append([]string{string(s.Role)}, s.ExtraArgs...),
			StandardOut:   filepath.Join(s.LogDir, string(s.Role)+".out.log"),
			StandardError: filepath.Join(s.LogDir, string(s.Role)+".err.log"),
		}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want.Bytes()) {
			t.Errorf("%s: render changed without a launcher\ngot:\n%s\nwant:\n%s", s.Role, got, want.Bytes())
		}
	}
}

func TestSpecRenderWithLauncher(t *testing.T) {
	s := Spec{
		Role:       RoleSource,
		Launcher:   "/Users/test/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher",
		BinaryPath: "/Users/test/bin/agentcookie",
		LogDir:     "/Users/test/.agentcookie/logs",
		ExtraArgs:  []string{"--watch"},
	}
	out, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	str := string(out)
	want := "<array>\n" +
		"    <string>/Users/test/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher</string>\n" +
		"    <string>/Users/test/bin/agentcookie</string>\n" +
		"    <string>source</string>\n" +
		"    <string>--watch</string>\n" +
		"  </array>"
	if !strings.Contains(str, want) {
		t.Errorf("ProgramArguments not launcher-first:\n%s", str)
	}
	if !strings.Contains(str, "<key>AssociatedBundleIdentifiers</key>\n  <array>\n    <string>dev.agentcookie.app</string>\n  </array>") {
		t.Errorf("missing AssociatedBundleIdentifiers:\n%s", str)
	}
	var parsed any
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Errorf("wrapped plist failed XML parse: %v", err)
	}
}

func TestSpecRenderEscapesPaths(t *testing.T) {
	s := Spec{
		Role:       RoleSource,
		Launcher:   "/Users/a&b/Applications/agentcookie.app/Contents/MacOS/agentcookie-launcher",
		BinaryPath: "/Users/a&b/<bin>/agentcookie",
		LogDir:     "/Users/a&b/.agentcookie/logs",
	}
	out, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "/Users/a&amp;b/&lt;bin&gt;/agentcookie") {
		t.Errorf("binary path not escaped:\n%s", out)
	}
	var parsed any
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Errorf("plist with & and < failed XML parse: %v", err)
	}
}
