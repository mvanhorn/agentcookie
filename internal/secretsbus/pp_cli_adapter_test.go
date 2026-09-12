package secretsbus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveManifestFromPP_TeslaShape(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{
		"schema_version": 1,
		"cli_name": "tesla-pp-cli",
		"display_name": "Tesla",
		"description": "Every Tesla mobile-API feature",
		"auth_env_vars": ["TESLA_AUTH_TOKEN"],
		"auth_env_var_specs": [
			{"name": "TESLA_AUTH_TOKEN", "kind": "per_call", "required": true, "sensitive": true, "description": "Tesla iOS-app OAuth bearer"}
		]
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if m.SchemaVersion != 2 {
		t.Errorf("schema_version: %d", m.SchemaVersion)
	}
	if m.Name != "tesla-pp-cli" {
		t.Errorf("name: %q", m.Name)
	}
	if m.DisplayName != "Tesla" {
		t.Errorf("display_name: %q", m.DisplayName)
	}
	if m.ProjectKind != "cli" {
		t.Errorf("project_kind: %q", m.ProjectKind)
	}
	// config.toml is TOML, not env-shaped, so it rides as a carried file
	// rather than a [secrets.file] read-in-place.
	if m.Secrets.File != nil {
		t.Errorf("[secrets.file] should not be set on a derived manifest: %#v", m.Secrets.File)
	}
	if len(m.Files) != 1 {
		t.Fatalf("want exactly 1 carried file, got %d: %#v", len(m.Files), m.Files)
	}
	f := m.Files[0]
	if f.Source != "~/.config/tesla-pp-cli/config.toml" {
		t.Errorf("source: %q", f.Source)
	}
	if f.Target != "tesla-pp-cli/config.toml" {
		t.Errorf("target: %q", f.Target)
	}
	if f.Key != "TESLA_PP_CLI_CONFIG_TOML" {
		t.Errorf("key: %q", f.Key)
	}
	if f.Optional {
		t.Error("derived config carriage must not be opt-in")
	}
	v, ok := m.Sync.Keys["TESLA_AUTH_TOKEN"]
	if !ok || !v {
		t.Errorf("TESLA_AUTH_TOKEN should be sync=true: %v ok=%v", v, ok)
	}
}

func TestDeriveManifestFromPP_NonSensitiveExcluded(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{
		"cli_name": "demo-cli",
		"display_name": "Demo",
		"auth_env_var_specs": [
			{"name": "SECRET_TOKEN", "sensitive": true},
			{"name": "BASE_URL", "sensitive": false}
		]
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if v := m.Sync.Keys["SECRET_TOKEN"]; !v {
		t.Errorf("sensitive should be true: %v", v)
	}
	if v := m.Sync.Keys["BASE_URL"]; v {
		t.Errorf("non-sensitive should be false: %v", v)
	}
}

func TestDeriveManifestFromPP_NoSpecsFallbackToEnvVars(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{
		"cli_name": "demo",
		"display_name": "Demo",
		"auth_env_vars": ["API_KEY", "API_SECRET"]
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if v := m.Sync.Keys["API_KEY"]; !v {
		t.Errorf("fallback should ship: %v", v)
	}
	if v := m.Sync.Keys["API_SECRET"]; !v {
		t.Errorf("fallback should ship: %v", v)
	}
}

func TestDeriveManifestFromPP_HyphenatedSlugYieldsValidCarryKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{
		"cli_name": "booking-com-pp-cli",
		"auth_env_var_specs": [{"name": "ACCESS_TOKEN", "sensitive": true}]
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(m.Files) != 1 {
		t.Fatalf("want 1 carried file, got %d", len(m.Files))
	}
	if got := m.Files[0].Key; got != "BOOKING_COM_PP_CLI_CONFIG_TOML" {
		t.Errorf("key: %q", got)
	}
	if !validKeyName(m.Files[0].Key) {
		t.Errorf("carry key %q must be a valid env key name", m.Files[0].Key)
	}
}

// espn ships preferences only (a [favorites] table, no credentials). The audit
// records it as having no secrets, so nothing should be carried for it.
func TestDeriveManifestFromPP_PreferenceOnlyCarriesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{
		"cli_name": "espn-pp-cli",
		"auth_env_var_specs": [
			{"name": "BASE_URL", "sensitive": false},
			{"name": "TIMEOUT", "sensitive": false}
		]
	}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(m.Files) != 0 {
		t.Errorf("preference-only CLI must carry nothing, got %#v", m.Files)
	}
	if m.Secrets.File != nil {
		t.Errorf("preference-only CLI must not declare [secrets.file]: %#v", m.Secrets.File)
	}
}

func TestDeriveManifestFromPP_FallbackEnvVarsStillCarry(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{"cli_name": "demo", "auth_env_vars": ["API_KEY"]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(m.Files) != 1 {
		t.Fatalf("legacy auth_env_vars treats all keys as shipped, so it must carry: %#v", m.Files)
	}
}

func TestDeriveManifestFromPP_MissingCliNameErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{"display_name": "No Slug"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DeriveManifestFromPP(p)
	if err == nil {
		t.Fatal("expected error on missing cli_name")
	}
}

func TestDeriveManifestFromPP_InvalidCliNameErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{"cli_name": "Bad Name"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DeriveManifestFromPP(p)
	if err == nil {
		t.Fatal("expected error on invalid slug")
	}
}

func TestDeriveManifestFromPP_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	if err := os.WriteFile(p, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DeriveManifestFromPP(p)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDeriveManifestFromPP_FileNotFound(t *testing.T) {
	_, err := DeriveManifestFromPP("/nonexistent/.printing-press.json")
	if err == nil {
		t.Fatal("expected file not found")
	}
}

func TestDeriveManifestFromPP_RoundTripsThroughV2Validator(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{"cli_name": "demo", "display_name": "Demo", "auth_env_vars": ["X"]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManifestV2(m, "derived"); err != nil {
		t.Errorf("derived manifest failed v2 validation: %v", err)
	}
}

func TestDeriveManifestFromPP_DisplayNameFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".printing-press.json")
	body := `{"cli_name": "demo"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := DeriveManifestFromPP(p)
	if err != nil {
		t.Fatal(err)
	}
	if m.DisplayName != "demo" {
		t.Errorf("display fallback: %q", m.DisplayName)
	}
}
