package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevoke_ExplicitManifestNeedsForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	manifestPath := filepath.Join(manDir, "demo.toml")
	os.WriteFile(manifestPath, []byte(`schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`), 0o600)

	revokeForce = false
	defer func() { revokeForce = false }()
	cmd := secretRevokeCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := runSecretRevoke(cmd, []string{"demo"})
	if err == nil {
		t.Error("expected error without --force")
	}
	if _, statErr := os.Stat(manifestPath); statErr != nil {
		t.Errorf("file should not be deleted without --force: %v", statErr)
	}
}

func TestRevoke_ExplicitManifestWithForceDeletes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	manifestPath := filepath.Join(manDir, "demo.toml")
	os.WriteFile(manifestPath, []byte(`schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`), 0o600)

	revokeForce = true
	defer func() { revokeForce = false }()
	cmd := secretRevokeCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := runSecretRevoke(cmd, []string{"demo"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := os.Stat(manifestPath); err == nil {
		t.Errorf("manifest should be deleted")
	}
}

func TestRevoke_LegacyV1RemovesBusDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	busDir := filepath.Join(home, ".agentcookie", "secrets", "legacy")
	os.MkdirAll(busDir, 0o700)
	os.WriteFile(filepath.Join(busDir, "secrets.env"), []byte("X=Y\n"), 0o600)

	cmd := secretRevokeCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := runSecretRevoke(cmd, []string{"legacy"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := os.Stat(busDir); err == nil {
		t.Errorf("bus dir should be removed")
	}
}

func TestRevoke_PPDerivedPrintsInstructions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	libDir := filepath.Join(home, "printing-press", "library", "tesla")
	os.MkdirAll(libDir, 0o700)
	os.WriteFile(filepath.Join(libDir, ".printing-press.json"), []byte(`{"cli_name":"tesla-pp-cli","display_name":"Tesla","auth_env_vars":["TOKEN"]}`), 0o644)

	cmd := secretRevokeCmd
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})
	if err := runSecretRevoke(cmd, []string{"tesla-pp-cli"}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "silence") {
		t.Errorf("output should mention silence: %q", out)
	}
	if !strings.Contains(out, "sync.default = false") {
		t.Errorf("output should include sync.default = false: %q", out)
	}
}

func TestRevoke_UnknownNameErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmd := secretRevokeCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := runSecretRevoke(cmd, []string{"nonexistent"})
	if err == nil {
		t.Error("expected error for unknown name")
	}
	if !strings.Contains(err.Error(), "no such") {
		t.Errorf("error message: %v", err)
	}
}

func TestRevoke_InvalidName(t *testing.T) {
	cmd := secretRevokeCmd
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := runSecretRevoke(cmd, []string{"Bad Name"})
	if err == nil {
		t.Error("expected error for invalid name")
	}
}
