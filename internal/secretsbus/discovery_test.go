package secretsbus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeExplicit(t *testing.T, dir, name, slug string) string {
	body := `schema_version = 2
name = "` + slug + `"
display_name = "Display ` + slug + `"
[secrets.file]
path = "~/.config/` + slug + `/.env"
`
	p := filepath.Join(dir, name)
	writeTestFile(t, p, body)
	return p
}

func writePP(t *testing.T, libDir, cliName string) string {
	dir := filepath.Join(libDir, cliName)
	body := `{"cli_name":"` + cliName + `","display_name":"Display ` + cliName + `","auth_env_vars":["TOKEN"]}`
	p := filepath.Join(dir, ".printing-press.json")
	writeTestFile(t, p, body)
	return p
}

func TestDiscover_ExplicitManifestOnly(t *testing.T) {
	home := t.TempDir()
	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "last30days.toml", "last30days")
	reg, errs := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")})
	if len(errs) > 0 {
		t.Errorf("unexpected errs: %v", errs)
	}
	if len(reg.Projects) != 1 {
		t.Fatalf("want 1 project, got %d: %#v", len(reg.Projects), reg.Projects)
	}
	rp, ok := reg.Projects["last30days"]
	if !ok {
		t.Fatal("last30days missing")
	}
	if rp.Kind != SourceKindExplicitManifest {
		t.Errorf("kind: %v", rp.Kind)
	}
	wantPath := filepath.Join(home, ".config", "last30days", ".env")
	if rp.ReadInPlacePath != wantPath {
		t.Errorf("read-in-place: %q want %q", rp.ReadInPlacePath, wantPath)
	}
}

func TestDiscover_PPDerivedOnly(t *testing.T) {
	home := t.TempDir()
	lib := filepath.Join(home, "printing-press", "library")
	writePP(t, lib, "tesla-pp-cli")
	reg, _ := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: lib, SystemPath: filepath.Join(home, "no-sys")})
	if len(reg.Projects) != 1 {
		t.Fatalf("want 1 project, got %d", len(reg.Projects))
	}
	rp, ok := reg.Projects["tesla-pp-cli"]
	if !ok {
		t.Fatal("tesla-pp-cli missing")
	}
	if rp.Kind != SourceKindPPCLIDerived {
		t.Errorf("kind: %v", rp.Kind)
	}
}

func TestDiscover_LegacyV1OnlyMakesSyntheticEntry(t *testing.T) {
	home := t.TempDir()
	legacyDir := filepath.Join(home, ".agentcookie", "secrets", "legacy-cli")
	writeTestFile(t, filepath.Join(legacyDir, "secrets.env"), "FOO=bar\n")
	reg, _ := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")})
	rp, ok := reg.Projects["legacy-cli"]
	if !ok {
		t.Fatal("legacy-cli missing")
	}
	if rp.Kind != SourceKindLegacyV1 {
		t.Errorf("kind: %v", rp.Kind)
	}
}

func TestDiscover_ExplicitOverridesPPDerived(t *testing.T) {
	home := t.TempDir()
	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "tesla-pp-cli.toml", "tesla-pp-cli")
	lib := filepath.Join(home, "printing-press", "library")
	writePP(t, lib, "tesla-pp-cli")
	reg, _ := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: lib, SystemPath: filepath.Join(home, "no-sys")})

	exp, ok := reg.Projects["tesla-pp-cli"]
	if !ok {
		t.Fatal("explicit tesla-pp-cli missing")
	}
	if exp.Kind != SourceKindExplicitManifest {
		t.Errorf("explicit should win; got %v", exp.Kind)
	}
	derived, ok := reg.Projects["tesla-pp-cli-pp"]
	if !ok {
		t.Fatal("expected derived to be suffixed to tesla-pp-cli-pp")
	}
	if derived.Kind != SourceKindPPCLIDerived {
		t.Errorf("suffixed entry kind: %v", derived.Kind)
	}
}

func TestDiscover_TwoExplicitSameNameBothRejected(t *testing.T) {
	home := t.TempDir()
	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "demo.toml", "demo")
	writeExplicit(t, filepath.Join(home, ".config", "agentcookie", "manifests"), "demo.toml", "demo")
	reg, errs := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")})
	if _, ok := reg.Projects["demo"]; ok {
		t.Errorf("collision should reject both, got: %#v", reg.Projects["demo"])
	}
	if len(errs) == 0 {
		t.Error("expected collision error")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "collision") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'collision' in errors: %v", errs)
	}
}

func TestDiscover_EmptyHomeProducesEmptyRegistry(t *testing.T) {
	home := t.TempDir()
	reg, errs := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")})
	if len(reg.Projects) != 0 {
		t.Errorf("empty home should yield empty registry, got: %#v", reg.Projects)
	}
	if len(errs) != 0 {
		t.Errorf("no errors expected: %v", errs)
	}
}

func TestDiscover_MalformedManifestSoftSkipped(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, ".agentcookie", "manifests", "bad.toml"), "this is not valid = [[[[")
	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "good.toml", "good")
	reg, errs := Discover(DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")})
	if _, ok := reg.Projects["good"]; !ok {
		t.Error("good manifest should still load despite bad neighbor")
	}
	if len(errs) == 0 {
		t.Error("expected soft-skip error for bad.toml")
	}
	if len(reg.Skipped) == 0 {
		t.Error("expected skipped entry for bad.toml")
	}
}

func TestDiscover_IdempotentOnRepeatedRuns(t *testing.T) {
	home := t.TempDir()
	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "a.toml", "a-proj")
	cfg := DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")}
	reg1, _ := Discover(cfg)
	reg2, _ := Discover(cfg)
	if len(reg1.Projects) != len(reg2.Projects) {
		t.Errorf("not idempotent: %d vs %d", len(reg1.Projects), len(reg2.Projects))
	}
	for name := range reg1.Projects {
		if _, ok := reg2.Projects[name]; !ok {
			t.Errorf("name %q present in first run but not second", name)
		}
	}
}

func TestDiscoveryWatcher_DetectsNewManifest(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".agentcookie", "manifests"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := DiscoveryConfig{HomeDir: home, PPLibraryPath: filepath.Join(home, "no-pp"), SystemPath: filepath.Join(home, "no-sys")}
	deltas := make(chan RegistryDelta, 4)
	w := NewDiscoveryWatcher(cfg, 50*time.Millisecond, func(_ context.Context, d RegistryDelta, _ *Registry) {
		deltas <- d
	})
	ctx := t.Context()
	go w.Run(ctx)
	time.Sleep(150 * time.Millisecond) // let initial snapshot fire

	// Drain initial snapshot.
	select {
	case <-deltas:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("initial snapshot never fired")
	}

	writeExplicit(t, filepath.Join(home, ".agentcookie", "manifests"), "live.toml", "live-proj")

	select {
	case d := <-deltas:
		found := false
		for _, n := range d.Added {
			if n == "live-proj" {
				found = true
			}
		}
		if !found {
			t.Errorf("live-proj not in Added: %v", d.Added)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delta fired after writing new manifest")
	}
}
