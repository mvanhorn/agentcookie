package config

import (
	"path/filepath"
	"testing"
)

func TestDestinationDefaultsAndValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, kind := range []string{"live_chrome", "attach", "ppcli"} {
		c := &SinkConfig{Destination: &DestinationRef{Kind: kind}}
		d, e := c.ResolveDestinations()
		if e != nil || len(d) != 1 || d[0].Kind != kind {
			t.Fatalf("%s: %v %v", kind, d, e)
		}
	}
	c := &SinkConfig{Destination: &DestinationRef{Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: filepath.Join(home, ".config/google-chrome")}}}
	if _, e := c.ResolveDestinations(); e == nil {
		t.Fatal("default profile allowed")
	}
	c = &SinkConfig{Destination: &DestinationRef{Kind: "live_chrome"}, Destinations: []DestinationRef{{Kind: "live_chrome"}}}
	if _, e := c.ResolveDestinations(); e == nil {
		t.Fatal("ambiguous configuration accepted")
	}
	c = &SinkConfig{LiveCDP: LiveCDPRef{Enabled: true}}
	d, e := c.ResolveDestinations()
	if e != nil || d[0].Kind != "attach" {
		t.Fatal("legacy attach mapping")
	}
}

func TestDestinationRejectsDefaultProfileAncestors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{string(filepath.Separator), home, filepath.Join(home, ".config"), filepath.Join(home, ".config", "google-chrome", "Default")} {
		cfg := &SinkConfig{Destination: &DestinationRef{Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: path}}}
		if _, err := cfg.ResolveDestinations(); err == nil {
			t.Fatalf("unsafe ancestor/descendant allowed: %s", path)
		}
	}
	if DefaultProfileOverlap(filepath.Join(home, "dedicated-chrome"), home) {
		t.Fatal("dedicated sibling falsely rejected")
	}
}
