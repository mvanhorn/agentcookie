package agentcookiesecret

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a t.Helper for tests; mkdir parents, write 0600.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoad_InvalidCLIName(t *testing.T) {
	_, err := Load("Bad-Name")
	if !errors.Is(err, ErrInvalidCLIName) {
		t.Errorf("expected ErrInvalidCLIName, got: %v", err)
	}
	_, err = Load("../etc")
	if !errors.Is(err, ErrInvalidCLIName) {
		t.Errorf("expected ErrInvalidCLIName, got: %v", err)
	}
}

func TestLoad_PlaintextBusOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env"),
		"FOO=from-bus\nBAR=also-bus\n")

	env, err := Load("demo-cli")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if env["FOO"] != "from-bus" || env["BAR"] != "also-bus" {
		t.Errorf("missing keys: %v", env)
	}
}

func TestLoad_BusWinsOverEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OAUTH_BEARER", "from-env-stale")
	writeFile(t, filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env"),
		"OAUTH_BEARER=from-bus-fresh\n")

	res, err := LoadDetailed("demo-cli", "")
	if err != nil {
		t.Fatalf("LoadDetailed: %v", err)
	}
	if res.Env["OAUTH_BEARER"] != "from-bus-fresh" {
		t.Errorf("bus should win over env, got: %q", res.Env["OAUTH_BEARER"])
	}
	if res.Sources["OAUTH_BEARER"] != SourceBusPlain {
		t.Errorf("source should be SourceBusPlain, got: %v", res.Sources["OAUTH_BEARER"])
	}
}

func TestLoad_FallbackBetweenBusAndEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FROM_ENV_ONLY", "env-val")

	fallbackPath := filepath.Join(home, "legacy-config.env")
	writeFile(t, fallbackPath, "FROM_FALLBACK=fallback-val\nFROM_ENV_ONLY=fallback-overrides-env\n")

	writeFile(t, filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env"),
		"FROM_BUS=bus-val\nFROM_FALLBACK=bus-overrides-fallback\n")

	res, err := LoadDetailed("demo-cli", fallbackPath)
	if err != nil {
		t.Fatalf("LoadDetailed: %v", err)
	}
	if res.Env["FROM_BUS"] != "bus-val" || res.Sources["FROM_BUS"] != SourceBusPlain {
		t.Errorf("bus key wrong: %v / %v", res.Env["FROM_BUS"], res.Sources["FROM_BUS"])
	}
	if res.Env["FROM_FALLBACK"] != "bus-overrides-fallback" || res.Sources["FROM_FALLBACK"] != SourceBusPlain {
		t.Errorf("bus should override fallback")
	}
	if res.Env["FROM_ENV_ONLY"] != "fallback-overrides-env" || res.Sources["FROM_ENV_ONLY"] != SourceFallback {
		t.Errorf("fallback should override env when bus has no entry")
	}
}

func TestLoad_NoBusEntryFallsThroughToEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STANDALONE_VAR", "shell-set")

	res, err := LoadDetailed("nonexistent-cli", "")
	if err != nil {
		t.Fatalf("LoadDetailed: %v", err)
	}
	if res.Env["STANDALONE_VAR"] != "shell-set" {
		t.Errorf("env should still come through when no bus entry")
	}
	if res.Sources["STANDALONE_VAR"] != SourceEnv {
		t.Errorf("source should be SourceEnv")
	}
}

func TestLoad_SealedFilePresentNoMasterKey(t *testing.T) {
	// Place a sealed file (with arbitrary bytes; this test doesn't need
	// real ciphertext, since ReadMasterKey will return ErrMasterKeyMissing
	// first). We exercise the error path where sealed exists but master
	// key is unavailable.
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".agentcookie", "secrets", "demo-cli", "secrets.env.sealed"),
		"opaque-sealed-bytes")

	_, err := Load("demo-cli")
	if err == nil {
		t.Errorf("expected error when sealed file present but master key missing")
	}
	if !strings.Contains(err.Error(), "master key") {
		t.Errorf("error should mention master key, got: %v", err)
	}
}

func TestParseEnvScanner_QuotesStripped(t *testing.T) {
	got, err := parseEnvBytes([]byte("A=\"hello world\"\nB='also quoted'\nC=plain\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["A"] != "hello world" || got["B"] != "also quoted" || got["C"] != "plain" {
		t.Errorf("quote handling: %v", got)
	}
}

func TestParseEnvScanner_BackslashContinuation(t *testing.T) {
	got, err := parseEnvBytes([]byte("LONG=part1\\\npart2\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["LONG"] != "part1part2" {
		t.Errorf("continuation: %q", got["LONG"])
	}
}

func TestParseEnvScanner_RejectsWhitespaceAroundEquals(t *testing.T) {
	_, err := parseEnvBytes([]byte("KEY = value\n"))
	if err == nil {
		t.Errorf("whitespace around = should reject")
	}
}

func TestValidCLIName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"foo", true},
		{"foo-bar", true},
		{"foo-pp-cli", true},
		{"Foo", false},
		{"foo_bar", false},
		{"foo.bar", false},
		{"-foo", false},
		{"foo-", false},
		{"", false},
	} {
		if got := validCLIName(tc.in); got != tc.want {
			t.Errorf("validCLIName(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}
