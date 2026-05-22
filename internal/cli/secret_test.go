package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretList_EmptyBus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buf := &bytes.Buffer{}
	cmd := secretListCmd
	cmd.SetOut(buf)
	if err := runSecretList(cmd, nil); err != nil {
		t.Fatalf("runSecretList: %v", err)
	}
	if !strings.Contains(buf.String(), "empty") {
		t.Errorf("expected 'empty' message, got: %q", buf.String())
	}
}

func TestSecretList_PopulatedBus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cliDir := filepath.Join(home, ".agentcookie", "secrets", "demo-cli")
	if err := os.MkdirAll(cliDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliDir, "secrets.env"), []byte("K1=v1\nK2=v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	cmd := secretListCmd
	cmd.SetOut(buf)
	if err := runSecretList(cmd, nil); err != nil {
		t.Fatalf("runSecretList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "demo-cli") || !strings.Contains(out, "K1") || !strings.Contains(out, "K2") {
		t.Errorf("list output incomplete: %q", out)
	}
	// Values must NOT leak in list output.
	if strings.Contains(out, "v1") || strings.Contains(out, "v2") {
		t.Errorf("list should not print values: %q", out)
	}
}

func TestSecretGet_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cliDir := filepath.Join(home, ".agentcookie", "secrets", "demo-cli")
	os.MkdirAll(cliDir, 0o700)
	os.WriteFile(filepath.Join(cliDir, "secrets.env"), []byte("TOKEN=abc-123\n"), 0o600)

	buf := &bytes.Buffer{}
	cmd := secretGetCmd
	cmd.SetOut(buf)
	if err := runSecretGet(cmd, []string{"demo-cli", "TOKEN"}); err != nil {
		t.Fatalf("runSecretGet: %v", err)
	}
	if buf.String() != "abc-123" {
		t.Errorf("get output: %q", buf.String())
	}
}

func TestSecretGet_KeyMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cliDir := filepath.Join(home, ".agentcookie", "secrets", "demo-cli")
	os.MkdirAll(cliDir, 0o700)
	os.WriteFile(filepath.Join(cliDir, "secrets.env"), []byte("A=1\n"), 0o600)

	cmd := secretGetCmd
	cmd.SetOut(&bytes.Buffer{})
	err := runSecretGet(cmd, []string{"demo-cli", "MISSING"})
	if err == nil {
		t.Errorf("missing key should error")
	}
}

func TestSecretSet_StdinPipe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Pipe stdin via a temp file because runSecretSet reads os.Stdin
	// directly. We replace os.Stdin for the duration.
	r, w, _ := os.Pipe()
	w.Write([]byte("piped-value\n"))
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig }()

	buf := &bytes.Buffer{}
	cmd := secretSetCmd
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})
	if err := runSecretSet(cmd, []string{"demo-cli", "MYKEY"}); err != nil {
		t.Fatalf("runSecretSet: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(stored), "MYKEY=piped-value") {
		t.Errorf("file content: %q", string(stored))
	}
}

func TestSecretRm_SingleKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cliDir := filepath.Join(home, ".agentcookie", "secrets", "demo-cli")
	os.MkdirAll(cliDir, 0o700)
	os.WriteFile(filepath.Join(cliDir, "secrets.env"), []byte("A=1\nB=2\n"), 0o600)

	cmd := secretRmCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := runSecretRm(cmd, []string{"demo-cli", "A"}); err != nil {
		t.Fatalf("runSecretRm: %v", err)
	}
	remaining, _ := os.ReadFile(filepath.Join(cliDir, "secrets.env"))
	if strings.Contains(string(remaining), "A=") {
		t.Errorf("A should be removed: %q", string(remaining))
	}
	if !strings.Contains(string(remaining), "B=2") {
		t.Errorf("B should remain: %q", string(remaining))
	}
}

func TestSecretRm_WholeCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cliDir := filepath.Join(home, ".agentcookie", "secrets", "demo-cli")
	os.MkdirAll(cliDir, 0o700)
	os.WriteFile(filepath.Join(cliDir, "secrets.env"), []byte("X=Y\n"), 0o600)

	cmd := secretRmCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := runSecretRm(cmd, []string{"demo-cli"}); err != nil {
		t.Fatalf("runSecretRm: %v", err)
	}
	if _, err := os.Stat(cliDir); err == nil {
		t.Errorf("cliDir should be removed")
	}
}

func TestSecretImportFrom_JSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "tesla-auth.json")
	os.WriteFile(src, []byte(`{"access_token":"AT","refresh_token":"RT","expires_at":"2026-01-01T00:00:00Z"}`), 0o600)

	cmd := secretImportFromCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	secretImportAs = "tesla-pp-cli"
	defer func() { secretImportAs = "" }()
	if err := runSecretImportFrom(cmd, []string{src}); err != nil {
		t.Fatalf("import: %v", err)
	}
	written, _ := os.ReadFile(filepath.Join(home, ".agentcookie", "secrets", "tesla-pp-cli", "secrets.env"))
	s := string(written)
	if !strings.Contains(s, "OAUTH_BEARER=AT") {
		t.Errorf("access_token should canonicalize to OAUTH_BEARER: %q", s)
	}
	if !strings.Contains(s, "OAUTH_REFRESH=RT") {
		t.Errorf("refresh_token should canonicalize to OAUTH_REFRESH: %q", s)
	}
}

func TestSecretImportFrom_UnknownFieldGetsReservedPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "config.json")
	os.WriteFile(src, []byte(`{"some-weird-field":"value"}`), 0o600)

	cmd := secretImportFromCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	secretImportAs = "demo-cli"
	defer func() { secretImportAs = "" }()
	if err := runSecretImportFrom(cmd, []string{src}); err != nil {
		t.Fatalf("import: %v", err)
	}
	written, _ := os.ReadFile(filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env"))
	if !strings.Contains(string(written), "_unknown_some_weird_field=value") {
		t.Errorf("unknown field should land under _unknown_ prefix: %q", string(written))
	}
}

func TestValidBusName_RejectsTraversal(t *testing.T) {
	if validBusName("../etc") {
		t.Errorf("traversal should be rejected")
	}
	if validBusName("Foo") {
		t.Errorf("uppercase should be rejected")
	}
}
