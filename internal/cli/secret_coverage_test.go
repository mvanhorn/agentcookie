package cli

import (
	"bytes"
	"testing"
)

func TestSecretCoverage_InPlaceWhenNoStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if status, _ := secretCoverage("no-store-cli", []string{"TESLA_AUTH_TOKEN"}); status != "in-place" {
		t.Errorf("no secret store should be in-place, got %q", status)
	}
}

func TestSecretCoverage_OKWhenDeclaredPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "good-cli", "TESLA_AUTH_TOKEN=tok\n")
	if status, _ := secretCoverage("good-cli", []string{"TESLA_AUTH_TOKEN"}); status != "ok" {
		t.Errorf("declared key present should be ok, got %q", status)
	}
}

func TestSecretCoverage_MismatchWhenDeclaredMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "tesla-pp-cli", "OAUTH_BEARER=tok\nOAUTH_REFRESH=ref\n")
	status, detail := secretCoverage("tesla-pp-cli", []string{"TESLA_AUTH_TOKEN"})
	if status != "MISMATCH" {
		t.Fatalf("store without declared key should be MISMATCH, got %q", status)
	}
	if detail == "" {
		t.Error("MISMATCH should carry remediation detail")
	}
}

func TestSecretCoverage_AliasSatisfies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSecrets(t, home, "tesla-pp-cli", "OAUTH_BEARER=tok\n")
	cmd := secretAliasCmd
	cmd.SetOut(&bytes.Buffer{})
	if err := runSecretAlias(cmd, []string{"tesla-pp-cli", "TESLA_AUTH_TOKEN", "OAUTH_BEARER"}); err != nil {
		t.Fatalf("set alias: %v", err)
	}
	if status, _ := secretCoverage("tesla-pp-cli", []string{"TESLA_AUTH_TOKEN"}); status != "ok" {
		t.Errorf("alias should satisfy declared key, got %q", status)
	}
}
