package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDiscover_EmptyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buf := &bytes.Buffer{}
	discoverCmd.SetOut(buf)
	if err := runDiscover(discoverCmd, nil); err != nil {
		t.Fatalf("runDiscover: %v", err)
	}
	if !strings.Contains(buf.String(), "no adopted projects") {
		t.Errorf("expected empty message, got: %q", buf.String())
	}
}

func TestRunDiscover_OneProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(`schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`), 0o600)

	buf := &bytes.Buffer{}
	discoverCmd.SetOut(buf)
	defer func() { common.JSON = false }()
	common.JSON = false
	if err := runDiscover(discoverCmd, nil); err != nil {
		t.Fatalf("runDiscover: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "demo") {
		t.Errorf("output missing demo: %q", out)
	}
	if !strings.Contains(out, "explicit-manifest") {
		t.Errorf("output missing tier: %q", out)
	}
}

func TestRunDiscover_JSONOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(`schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`), 0o600)

	common.JSON = true
	defer func() { common.JSON = false }()
	buf := &bytes.Buffer{}
	discoverCmd.SetOut(buf)
	if err := runDiscover(discoverCmd, nil); err != nil {
		t.Fatal(err)
	}
	var out discoverJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("json unmarshal: %v; output: %q", err, buf.String())
	}
	if len(out.Projects) != 1 {
		t.Errorf("want 1 project, got %d: %+v", len(out.Projects), out)
	}
	if out.Projects[0].Name != "demo" {
		t.Errorf("name: %q", out.Projects[0].Name)
	}
}

func TestRunDiscover_VerboseShowsSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "bad.toml"), []byte("not valid = [[[["), 0o600)

	discoverVerbose = true
	defer func() { discoverVerbose = false }()
	common.JSON = false
	buf := &bytes.Buffer{}
	discoverCmd.SetOut(buf)
	if err := runDiscover(discoverCmd, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "skipped:") {
		t.Errorf("verbose should include 'skipped:': %q", out)
	}
	if !strings.Contains(out, "bad.toml") {
		t.Errorf("skipped section should mention bad.toml: %q", out)
	}
}
