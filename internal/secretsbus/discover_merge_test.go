package secretsbus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPayloadWithDiscovery_V1OnlyStillWorks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := filepath.Join(home, ".agentcookie", "secrets", "v1cli")
	os.MkdirAll(d, 0o700)
	os.WriteFile(filepath.Join(d, "secrets.env"), []byte("A=1\nB=2\n"), 0o600)

	p, errs := LoadPayloadWithDiscovery(home)
	if len(errs) > 0 {
		t.Errorf("unexpected errs: %v", errs)
	}
	got, ok := p.CLIs["v1cli"]
	if !ok {
		t.Fatal("v1cli missing")
	}
	if got["A"] != "1" || got["B"] != "2" {
		t.Errorf("v1 values: %v", got)
	}
}

func TestLoadPayloadWithDiscovery_V2DiscoversAndReadsInPlace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// .env file the manifest points to
	envPath := filepath.Join(home, ".config", "demo", ".env")
	os.MkdirAll(filepath.Dir(envPath), 0o700)
	os.WriteFile(envPath, []byte("TOKEN=abc\nBASE_URL=http://x\n"), 0o600)

	// Manifest at ~/.agentcookie/manifests/demo.toml
	manifest := `schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(manifest), 0o600)

	p, errs := LoadPayloadWithDiscovery(home)
	if len(errs) > 0 {
		t.Errorf("unexpected errs: %v", errs)
	}
	got, ok := p.CLIs["demo"]
	if !ok {
		t.Fatalf("demo missing; got: %#v", p.CLIs)
	}
	if got["TOKEN"] != "abc" {
		t.Errorf("TOKEN: %q", got["TOKEN"])
	}
}

func TestLoadPayloadWithDiscovery_V1WinsOverV2(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// v1 bus has TOKEN=v1
	v1Dir := filepath.Join(home, ".agentcookie", "secrets", "demo")
	os.MkdirAll(v1Dir, 0o700)
	os.WriteFile(filepath.Join(v1Dir, "secrets.env"), []byte("TOKEN=v1\n"), 0o600)

	// v2 read-in-place has TOKEN=v2 AND EXTRA=yes
	envPath := filepath.Join(home, ".config", "demo", ".env")
	os.MkdirAll(filepath.Dir(envPath), 0o700)
	os.WriteFile(envPath, []byte("TOKEN=v2\nEXTRA=yes\n"), 0o600)
	manifest := `schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(manifest), 0o600)

	p, _ := LoadPayloadWithDiscovery(home)
	got := p.CLIs["demo"]
	if got["TOKEN"] != "v1" {
		t.Errorf("v1 should win on TOKEN; got %q", got["TOKEN"])
	}
	if got["EXTRA"] != "yes" {
		t.Errorf("v2-only EXTRA should be included; got %q", got["EXTRA"])
	}
}

func TestLoadPayloadWithDiscovery_ManifestSyncFilterApplied(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	envPath := filepath.Join(home, ".config", "demo", ".env")
	os.MkdirAll(filepath.Dir(envPath), 0o700)
	os.WriteFile(envPath, []byte("SECRET=yes\nCONFIG=keep_local\n"), 0o600)

	manifest := `schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
[sync.keys]
CONFIG = false
`
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(manifest), 0o600)

	p, _ := LoadPayloadWithDiscovery(home)
	got := p.CLIs["demo"]
	if got["SECRET"] != "yes" {
		t.Errorf("SECRET should ship: %v", got)
	}
	if _, ok := got["CONFIG"]; ok {
		t.Errorf("CONFIG should be filtered out: %v", got)
	}
}

func TestLoadPayloadWithDiscovery_MissingReadInPlaceFileSoftSkips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	manifest := `schema_version = 2
name = "demo"
display_name = "Demo"
[secrets.file]
path = "~/.config/demo/.env"
`
	manDir := filepath.Join(home, ".agentcookie", "manifests")
	os.MkdirAll(manDir, 0o700)
	os.WriteFile(filepath.Join(manDir, "demo.toml"), []byte(manifest), 0o600)

	p, errs := LoadPayloadWithDiscovery(home)
	if _, ok := p.CLIs["demo"]; ok {
		t.Errorf("missing file should produce no entry: %v", p.CLIs["demo"])
	}
	if len(errs) == 0 {
		t.Error("expected a soft-skip error for missing file")
	}
}

func TestLoadPayloadWithDiscovery_EmptyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, errs := LoadPayloadWithDiscovery(home)
	if len(errs) != 0 {
		t.Errorf("empty home: unexpected errors: %v", errs)
	}
	if len(p.CLIs) != 0 {
		t.Errorf("empty home should yield empty CLIs: %v", p.CLIs)
	}
}
