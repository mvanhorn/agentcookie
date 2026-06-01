package secretsbus

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifestV2_HappyPathFull(t *testing.T) {
	body := `
schema_version = 2
name = "last30days"
display_name = "last30days"
description = "Brand intelligence skill"
project_kind = "skill"
homepage = "https://github.com/mvanhorn/last30days-skill"

[secrets.file]
path = "~/.config/last30days/.env"

[sync]
default = true

[sync.keys]
SETUP_COMPLETE = false
FROM_BROWSER = false
`
	m, warnings, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if m.Name != "last30days" {
		t.Errorf("name: %q", m.Name)
	}
	if m.DisplayName != "last30days" {
		t.Errorf("display_name: %q", m.DisplayName)
	}
	if m.ProjectKind != "skill" {
		t.Errorf("project_kind: %q", m.ProjectKind)
	}
	if m.Secrets.File == nil || m.Secrets.File.Path != "~/.config/last30days/.env" {
		t.Errorf("[secrets.file].path: %#v", m.Secrets.File)
	}
	if !m.Sync.Default {
		t.Errorf("sync.default: %v", m.Sync.Default)
	}
	if v, ok := m.Sync.Keys["SETUP_COMPLETE"]; !ok || v {
		t.Errorf("sync.keys.SETUP_COMPLETE: %v ok=%v", v, ok)
	}
}

func TestParseManifestV2_Minimal(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.file]
path = "~/.config/demo/.env"
`
	m, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.Sync.Default {
		t.Errorf("omitted [sync] -> default should be true; got %v", m.Sync.Default)
	}
}

func TestParseManifestV2_UnknownFieldWarns(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"
unknown_future_field = "hello"

[secrets.file]
path = "~/.config/demo/.env"
`
	_, warnings, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(warnings) == 0 {
		t.Errorf("expected unknown-field warning")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "unknown_future_field") {
			found = true
		}
	}
	if !found {
		t.Errorf("unknown_future_field not in warnings: %v", warnings)
	}
}

func TestParseManifestV2_ParsesAliases(t *testing.T) {
	body := `
schema_version = 2
name = "tesla-pp-cli"
display_name = "Tesla"

[secrets.file]
path = "~/.config/tesla-pp-cli/config.toml"

[aliases]
TESLA_AUTH_TOKEN = "OAUTH_BEARER"
`
	m, warnings, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w.Message, "aliases") {
			t.Errorf("aliases should not warn as unknown: %v", w)
		}
	}
	if got := m.Aliases["TESLA_AUTH_TOKEN"]; got != "OAUTH_BEARER" {
		t.Errorf("alias = %q, want OAUTH_BEARER", got)
	}
}

func TestParseManifestV2_RejectsInvalidAliasName(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.file]
path = "~/.config/demo/.env"

[aliases]
"not a valid var" = "OAUTH_BEARER"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error rejecting invalid alias env var name")
	}
	if !strings.Contains(err.Error(), "aliases") {
		t.Errorf("error should reference aliases: %v", err)
	}
}

func TestParseManifestV2_RejectsV1Schema(t *testing.T) {
	body := `
schema_version = 1
name = "demo"
display_name = "Demo"

[secrets.file]
path = "~/.config/demo/.env"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error rejecting v1 schema")
	}
	if !strings.Contains(err.Error(), "v1") {
		t.Errorf("error should reference v1: %v", err)
	}
}

func TestParseManifestV2_RejectsUnknownSchema(t *testing.T) {
	body := `
schema_version = 99
name = "demo"
display_name = "Demo"
[secrets.file]
path = "/tmp/foo"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error rejecting unknown schema")
	}
}

func TestParseManifestV2_RejectsMultipleSecretsBlocks(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.file]
path = "~/.config/demo/.env"

[secrets.command]
exec = ["echo", "hi"]
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on multi-block")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error should mention 'exactly one': %v", err)
	}
}

func TestParseManifestV2_RejectsCommandSourceKind(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.command]
exec = ["echo", "hi"]
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on command source")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("error should mention command: %v", err)
	}
}

func TestParseManifestV2_RejectsKeychainSourceKind(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.keychain]
service = "demo"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on keychain source")
	}
}

func TestParseManifestV2_RejectsNameTraversal(t *testing.T) {
	body := `
schema_version = 2
name = "../etc"
display_name = "Etc"

[secrets.file]
path = "~/.config/x"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on traversal name")
	}
}

func TestParseManifestV2_RejectsUppercaseName(t *testing.T) {
	body := `
schema_version = 2
name = "Foo"
display_name = "Foo"

[secrets.file]
path = "~/.config/x"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on uppercase name")
	}
}

func TestParseManifestV2_RejectsPathTraversal(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"

[secrets.file]
path = "~/.config/../../../etc/passwd"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on path traversal")
	}
}

func TestParseManifestV2_RejectsEmptyName(t *testing.T) {
	body := `
schema_version = 2
display_name = "Demo"
[secrets.file]
path = "~/x"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on empty name")
	}
}

func TestParseManifestV2_RejectsBadProjectKind(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"
project_kind = "wat"

[secrets.file]
path = "~/x"
`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected error on bad project_kind")
	}
}

func TestParseManifestV2_FileNotFound(t *testing.T) {
	_, _, err := ParseManifestV2("/nonexistent/path/agentcookie.toml")
	if !errors.Is(err, ErrManifestNotFound) {
		t.Errorf("expected ErrManifestNotFound, got: %v", err)
	}
}

func TestParseManifestV2_MalformedToml(t *testing.T) {
	body := `this is not toml = [[[`
	_, _, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestParseManifestV2_SignedByWarns(t *testing.T) {
	body := `
schema_version = 2
name = "demo"
display_name = "Demo"
signed_by = "matt@example.com"
[secrets.file]
path = "~/x"
`
	_, warnings, err := parseManifestV2Bytes([]byte(body), "test.toml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Message, "signed_by") && strings.Contains(w.Message, "v2.1") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected signed_by reserved warning, got: %v", warnings)
	}
}

func TestResolveSecretsPath(t *testing.T) {
	home := "/Users/test"
	tests := []struct {
		in, want string
	}{
		{"~/.config/foo/.env", "/Users/test/.config/foo/.env"},
		{"~", "/Users/test"},
		{"/etc/foo", "/etc/foo"},
	}
	for _, tc := range tests {
		m := &ManifestV2{Secrets: ManifestV2Secrets{File: &ManifestV2SecretsFile{Path: tc.in}}}
		got := m.ResolveSecretsPath(home)
		if got != tc.want {
			t.Errorf("ResolveSecretsPath(%q): got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestShouldShipKey(t *testing.T) {
	m := &ManifestV2{Sync: ManifestV2Sync{Default: true, Keys: map[string]bool{"NO_SHIP": false, "YES_SHIP": true}}}
	if !m.ShouldShipKey("ANY") {
		t.Errorf("default=true unknown key should ship")
	}
	if m.ShouldShipKey("NO_SHIP") {
		t.Errorf("explicit false override should not ship")
	}
	if !m.ShouldShipKey("YES_SHIP") {
		t.Errorf("explicit true override should ship")
	}
}

func TestShouldShipKey_DefaultFalse(t *testing.T) {
	m := &ManifestV2{Sync: ManifestV2Sync{Default: false, Keys: map[string]bool{"YES_SHIP": true}}}
	if m.ShouldShipKey("OTHER") {
		t.Errorf("default=false unknown key should not ship")
	}
	if !m.ShouldShipKey("YES_SHIP") {
		t.Errorf("explicit true override should ship even when default=false")
	}
}

func TestParseManifestV2_FromDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "agentcookie.toml")
	body := `
schema_version = 2
name = "diskdemo"
display_name = "Disk Demo"
[secrets.file]
path = "~/x"
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _, err := ParseManifestV2(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "diskdemo" {
		t.Errorf("name: %q", m.Name)
	}
}
