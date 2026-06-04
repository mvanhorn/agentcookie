package cmuxconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

func writeCfg(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cmux.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// uncommentedMode finds an uncommented socketControlMode value.
var uncommentedMode = regexp.MustCompile(`(?m)^\s*"socketControlMode"\s*:\s*"([^"]*)"`)

func mustMode(t *testing.T, src string) string {
	t.Helper()
	m := uncommentedMode.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no uncommented socketControlMode in:\n%s", src)
	}
	return m[1]
}

func TestSetSocketControlMode_FreshTemplate(t *testing.T) {
	// Case 3: the shipped template has automation fully commented out.
	p := writeCfg(t, `{
  "$schema": "https://raw.githubusercontent.com/manaflow-ai/cmux/main/web/data/cmux.schema.json",
  "schemaVersion": 1,
  //   "automation" : {
  //     "socketControlMode" : "cmuxOnly",
  //   },
}
`)
	bak, err := SetSocketControlMode(p, "allowAll", "", fixedTime)
	if err != nil {
		t.Fatalf("SetSocketControlMode: %v", err)
	}
	out := read(t, p)
	if got := mustMode(t, out); got != "allowAll" {
		t.Errorf("mode = %q, want allowAll", got)
	}
	// schema line + comments preserved.
	if !regexp.MustCompile(`\$schema`).MatchString(out) {
		t.Errorf("schema line dropped")
	}
	if _, err := os.Stat(bak); err != nil {
		t.Errorf("backup not created: %v", err)
	}
}

func TestSetSocketControlMode_ExistingKey(t *testing.T) {
	// Case 1: an uncommented value gets replaced; file otherwise stable.
	p := writeCfg(t, `{
  "automation": {
    "socketControlMode": "cmuxOnly",
    "portBase": 9100
  }
}
`)
	if _, err := SetSocketControlMode(p, "allowAll", "", fixedTime); err != nil {
		t.Fatalf("SetSocketControlMode: %v", err)
	}
	out := read(t, p)
	if got := mustMode(t, out); got != "allowAll" {
		t.Errorf("mode = %q, want allowAll", got)
	}
	if !regexp.MustCompile(`"portBase"\s*:\s*9100`).MatchString(out) {
		t.Errorf("unrelated setting portBase was lost:\n%s", out)
	}
}

func TestSetSocketControlMode_ExistingBlockNoKey(t *testing.T) {
	// Case 2: automation block exists but lacks socketControlMode.
	p := writeCfg(t, `{
  "automation": {
    "portBase": 9100
  }
}
`)
	if _, err := SetSocketControlMode(p, "allowAll", "", fixedTime); err != nil {
		t.Fatalf("SetSocketControlMode: %v", err)
	}
	out := read(t, p)
	if got := mustMode(t, out); got != "allowAll" {
		t.Errorf("mode = %q, want allowAll", got)
	}
}

func TestSetSocketControlMode_PasswordMode(t *testing.T) {
	p := writeCfg(t, `{
  "schemaVersion": 1
}
`)
	if _, err := SetSocketControlMode(p, "password", "s3cr3t", fixedTime); err != nil {
		t.Fatalf("SetSocketControlMode: %v", err)
	}
	out := read(t, p)
	if got := mustMode(t, out); got != "password" {
		t.Errorf("mode = %q, want password", got)
	}
	if !regexp.MustCompile(`"socketPassword"\s*:\s*"s3cr3t"`).MatchString(out) {
		t.Errorf("socketPassword not set:\n%s", out)
	}
}

func TestSetSocketControlMode_Idempotent(t *testing.T) {
	p := writeCfg(t, `{
  "automation": {
    "socketControlMode": "allowAll"
  }
}
`)
	if _, err := SetSocketControlMode(p, "allowAll", "", fixedTime); err != nil {
		t.Fatalf("SetSocketControlMode: %v", err)
	}
	// exactly one uncommented socketControlMode (no duplicate block)
	if n := len(uncommentedMode.FindAllString(read(t, p), -1)); n != 1 {
		t.Errorf("expected 1 socketControlMode, got %d (duplicate injected?)", n)
	}
}

func TestSetSocketControlMode_MissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope", "cmux.json")
	if _, err := SetSocketControlMode(p, "allowAll", "", fixedTime); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
