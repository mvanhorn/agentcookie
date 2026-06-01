package secretsbus

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPayloadWithDiscovery_CarriesFilesOnlyManifest covers the fix for a
// [[files]]-only manifest (no [secrets.file], so ReadInPlacePath==""): its
// carried files must still ship, where previously the empty read-in-place path
// short-circuited the loop before reaching the carry.
func TestLoadPayloadWithDiscovery_CarriesFilesOnlyManifest(t *testing.T) {
	home := t.TempDir()
	// A source file to carry.
	cfgDir := filepath.Join(home, ".config", "demo-cli")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("k = \"v\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A [[files]]-only manifest at the priority-1 discovery path.
	mdir := filepath.Join(home, ".agentcookie", "manifests")
	if err := os.MkdirAll(mdir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `
schema_version = 2
name = "demo-cli"
display_name = "Demo"

[[files]]
source = "~/.config/demo-cli/config.toml"
key = "DEMO_CONFIG"
target = "demo-cli/config.toml"
env = "DEMO_CONFIG_PATH"
`
	if err := os.WriteFile(filepath.Join(mdir, "demo-cli.toml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	p, _ := LoadPayloadWithDiscovery(home)
	kv := p.CLIs["demo-cli"]
	if kv["DEMO_CONFIG"] == "" {
		t.Fatalf("[[files]]-only manifest did not carry the file; payload keys: %v", keysOf(kv))
	}
	if kv[CarryFileKey("DEMO_CONFIG")] != "demo-cli/config.toml" {
		t.Errorf("missing target companion: %v", keysOf(kv))
	}
	if kv[CarryFileEnvKey("DEMO_CONFIG")] != "DEMO_CONFIG_PATH" {
		t.Errorf("missing env companion: %v", keysOf(kv))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
