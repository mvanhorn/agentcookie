package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveSourceRecomputesOnlyDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadSourceLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := cfg.Chrome.DBPath
	got, err := EffectiveSource(cfg, SourceOverrides{Browser: "brave", Profile: "Profile 2"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := SourceBrowserCookiesPath("brave", "Profile 2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Chrome.DBPath != want || got.Chrome.DBPath == original || cfg.Chrome.DBPath != original {
		t.Fatalf("effective=%q original=%q want=%q", got.Chrome.DBPath, cfg.Chrome.DBPath, want)
	}
	cfg.Chrome.DBPath = "/fixture/custom/Cookies"
	got, err = EffectiveSource(cfg, SourceOverrides{Browser: "brave"})
	if err != nil || got.Chrome.DBPath != cfg.Chrome.DBPath {
		t.Fatalf("explicit path lost: %#v %v", got, err)
	}
}
func TestEffectiveSourceCDPConflict(t *testing.T) {
	cfg := &SourceConfig{CDPSource: CDPSourceRef{Enabled: true, Endpoint: "http://127.0.0.1:9230"}}
	for _, over := range []SourceOverrides{{Browser: "chrome"}, {Profile: "Default"}, {Reader: "file"}} {
		if _, err := EffectiveSource(cfg, over); err == nil {
			t.Fatalf("accepted CDP override %#v", over)
		}
	}
}
func TestEffectiveSourceOverridePrecedesResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.yaml"), []byte("browser:\n  name: obsolete-browser\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSourceLocalWithOverrides(dir, SourceOverrides{Browser: "chrome"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Browser.Name != "chrome" {
		t.Fatal(got.Browser.Name)
	}
}
