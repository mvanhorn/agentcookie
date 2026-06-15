package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReportsCookiePolicy(t *testing.T) {
	dir := t.TempDir()
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: "example.com"
`)

	oldDir := common.ConfigDir
	oldJSON := common.JSON
	common.ConfigDir = dir
	common.JSON = false
	t.Cleanup(func() {
		common.ConfigDir = oldDir
		common.JSON = oldJSON
	})

	out := captureStdout(t, func() {
		if err := statusCmd.RunE(commandWithOutput(&bytes.Buffer{}), nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "cookie policy: allowlist") {
		t.Fatalf("status output should report allowlist policy, got %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return buf.String()
}
