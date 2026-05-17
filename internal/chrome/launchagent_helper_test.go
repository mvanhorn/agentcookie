package chrome

import (
	"strings"
	"testing"
)

func TestPlistEscape_AllFiveXMLEntities(t *testing.T) {
	in := `a&b<c>d"e'f`
	got := plistEscape(in)
	want := `a&amp;b&lt;c&gt;d&quot;e&apos;f`
	if got != want {
		t.Errorf("plistEscape: got %q, want %q", got, want)
	}
}

func TestPlistEscape_NoEscapeNeeded(t *testing.T) {
	in := "/Users/me/bin/agentcookie"
	if got := plistEscape(in); got != in {
		t.Errorf("plistEscape passthrough: got %q, want %q", got, in)
	}
}

func TestRenderOneShotPlist_HasRequiredKeys(t *testing.T) {
	got := renderOneShotPlist("dev.agentcookie.test", []string{"/bin/echo", "hello"}, "/tmp/out", "/tmp/err")
	for _, must := range []string{
		"<key>Label</key>", "dev.agentcookie.test",
		"<key>ProgramArguments</key>", "/bin/echo", "hello",
		"<key>RunAtLoad</key>", "<true/>",
		"<key>StandardOutPath</key>", "/tmp/out",
		"<key>StandardErrorPath</key>", "/tmp/err",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("plist missing %q. Full plist:\n%s", must, got)
		}
	}
}

func TestRenderOneShotPlist_EscapesArgvSpecialChars(t *testing.T) {
	// An argv string containing '&' (an injection candidate via plist) must
	// be escaped to &amp; or the plist becomes invalid XML.
	got := renderOneShotPlist("dev.agentcookie.test", []string{"/bin/sh", "-c", "echo a & b"}, "/tmp/o", "/tmp/e")
	if !strings.Contains(got, "echo a &amp; b") {
		t.Errorf("plist did not escape '&' in argv. Plist:\n%s", got)
	}
	if strings.Contains(got, "echo a & b") {
		t.Errorf("plist contains unescaped '&' in argv (will fail to parse). Plist:\n%s", got)
	}
}

func TestScanField_ParsesLaunchctlPrintOutput(t *testing.T) {
	sample := `	state = not running
	last exit code = 0
	pid = 12345
`
	if got := scanField(sample, "state ="); got != "not running" {
		t.Errorf("state field: got %q, want %q", got, "not running")
	}
	if got := scanField(sample, "last exit code ="); got != "0" {
		t.Errorf("exit code field: got %q, want %q", got, "0")
	}
}

func TestScanField_MissingKey(t *testing.T) {
	if got := scanField("nothing here", "state ="); got != "" {
		t.Errorf("missing key should return empty, got %q", got)
	}
}

func TestRandomLabel_UniqueAcrossCalls(t *testing.T) {
	a, err := randomLabel("dev.agentcookie.test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomLabel("dev.agentcookie.test")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two consecutive randomLabel calls returned same value: %q", a)
	}
	if !strings.HasPrefix(a, "dev.agentcookie.test.") {
		t.Errorf("label missing prefix: %q", a)
	}
	if len(a) != len("dev.agentcookie.test.")+8 {
		t.Errorf("label length unexpected: %q (len=%d)", a, len(a))
	}
}
