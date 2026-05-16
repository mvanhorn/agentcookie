package chromemgr

import (
	"strings"
	"testing"
)

func TestParseDevToolsActivePort(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantPort int
		wantPath string
		wantErr  bool
	}{
		{"two lines", "59321\n/devtools/browser/abc-123\n", 59321, "/devtools/browser/abc-123", false},
		{"port only", "59321\n", 59321, "", false},
		{"trailing whitespace", "  59321  \n/devtools/browser/x\n", 59321, "/devtools/browser/x", false},
		{"empty", "", 0, "", true},
		{"bad port", "notanint\n/p\n", 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			port, wsPath, err := parseDevToolsActivePort([]byte(c.input))
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", c.input)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if port != c.wantPort {
				t.Errorf("port: got %d, want %d", port, c.wantPort)
			}
			if wsPath != c.wantPath {
				t.Errorf("path: got %q, want %q", wsPath, c.wantPath)
			}
		})
	}
}

func TestConfigDefaultChromeBinary(t *testing.T) {
	c := Config{ProfileDir: "/tmp/x"}
	if !strings.Contains(c.chromeBinary(), "Google Chrome") {
		t.Errorf("default Chrome binary path looks wrong: %q", c.chromeBinary())
	}
}

func TestConfigStartupTimeoutDefault(t *testing.T) {
	c := Config{}
	if c.startupTimeout().Seconds() < 5 {
		t.Errorf("startup timeout default too low: %v", c.startupTimeout())
	}
}

func TestNewRequiresProfileDir(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Error("expected error for missing ProfileDir")
	}
}

func TestNewRejectsMissingChromeBinary(t *testing.T) {
	_, err := New(Config{
		ProfileDir:   t.TempDir(),
		ChromeBinary: "/nonexistent/Chrome",
	})
	if err == nil {
		t.Error("expected error for missing Chrome binary")
	}
}

func TestNewWithValidConfigSucceeds(t *testing.T) {
	// This test only validates the constructor path. It does not actually
	// start Chrome. Skip if Chrome is not installed at the default location.
	c := Config{ProfileDir: t.TempDir()}
	mgr, err := New(c)
	if err != nil {
		t.Skipf("Chrome not available at default path; skipping: %v", err)
	}
	if mgr.IsRunning() {
		t.Error("new manager should not report running before Start")
	}
}
