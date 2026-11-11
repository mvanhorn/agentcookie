package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNamedDestinationsIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := SinkConfig{Destinations: []DestinationRef{{Name: "personal", Kind: "live_chrome"}, {Name: "work", Kind: "live_chrome"}}}
	ds, err := cfg.ResolveDestinations()
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Chrome.Port == ds[1].Chrome.Port || ds[0].Chrome.UserDataDir == ds[1].Chrome.UserDataDir {
		t.Fatal("destinations share browser")
	}
	for i, name := range []string{"personal", "work"} {
		if ds[i].Chrome.UserDataDir != filepath.Join(home, ".agentcookie", "synced-chrome-"+name) {
			t.Fatalf("wrong profile: %+v", ds[i])
		}
	}
	for _, name := range []string{"@floor", "@v3", "../work", "WORK", "work/name", "work\\name", "work name", "work\nname", "."} {
		cfg.Destinations[0].Name = name
		if _, err := cfg.ResolveDestinations(); err == nil {
			t.Errorf("unsafe name %q accepted", name)
		}
	}
}
func TestDestinationsRejectOverlappingOrAliasedProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := filepath.Join(home, "owned")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{base, filepath.Join(base, "child"), alias, filepath.Join(alias, "child")} {
		cfg := SinkConfig{Destinations: []DestinationRef{{Name: "personal", Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: base}}, {Name: "work", Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: other}}}}
		if _, err := cfg.ResolveDestinations(); err == nil {
			t.Errorf("overlap accepted: %s", other)
		}
	}
	defaultDir := filepath.Join(home, ".config", "google-chrome")
	if err := os.MkdirAll(defaultDir, 0700); err != nil {
		t.Fatal(err)
	}
	defaultAlias := filepath.Join(home, "default-alias")
	if err := os.Symlink(defaultDir, defaultAlias); err != nil {
		t.Fatal(err)
	}
	cfg := SinkConfig{Destination: &DestinationRef{Name: "personal", Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: filepath.Join(defaultAlias, "new")}}}
	if _, err := cfg.ResolveDestinations(); err == nil {
		t.Fatal("aliased default profile accepted")
	}
}
func TestDestinationPortsReserveExplicitBeforeDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := SinkConfig{Destinations: []DestinationRef{{Name: "personal", Kind: "live_chrome"}, {Name: "work", Kind: "live_chrome", Chrome: DestinationChrome{Port: 9224}}}}
	ds, err := cfg.ResolveDestinations()
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Chrome.Port == ds[1].Chrome.Port {
		t.Fatal("default collided")
	}
}

func TestContainerSinkValidation(t *testing.T) {
	valid := []SinkTarget{{URL: "http://mini/sync", Peer: "mini", Destination: "personal", Containers: []string{"Personal"}}, {URL: "http://mini/sync", Peer: "mini", Destination: "work", Containers: []string{"Work"}}}
	if err := ValidateSinkTargets(valid); err != nil {
		t.Fatal(err)
	}
	for _, sinks := range [][]SinkTarget{
		{valid[0], valid[0]},
		{{URL: "http://mini/sync", Containers: []string{"Work"}}},
		{{URL: "http://mini/sync", Destination: "@floor"}},
		{{URL: "http://mini/sync", Destination: "work", Containers: []string{" "}}},
	} {
		if err := ValidateSinkTargets(sinks); err == nil {
			t.Fatalf("invalid targets accepted: %+v", sinks)
		}
	}
	if err := ValidateSinkTargets([]SinkTarget{{URL: "http://mini/sync", Peer: "mini"}}); err != nil {
		t.Fatal("legacy sink rejected", err)
	}
}

func TestOwnedProfilesRejectRootAndDefaultAncestors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, path := range []string{"/", home, filepath.Join(home, ".config"), filepath.Join(home, ".config/google-chrome"), filepath.Join(home, ".config/google-chrome/child")} {
		cfg := SinkConfig{Destination: &DestinationRef{Name: "work", Kind: "live_chrome", Chrome: DestinationChrome{UserDataDir: path}}}
		if _, err := cfg.ResolveDestinations(); err == nil {
			t.Errorf("default ancestor accepted: %q", path)
		}
	}
	if pathsOverlap("/tmp/one", "/tmp/one-more") {
		t.Fatal("sibling prefix overlaps")
	}
	if !profilePathsOverlap("/tmp/Accounts/Work", "/tmp/accounts/work/new", "darwin") {
		t.Fatal("Mac case-insensitive alias accepted")
	}
}
func TestAttachDestinationsRejectAliasesAndURLDecorations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, endpoint := range []string{"http://user@localhost:9224", "http://localhost:9224/path", "http://localhost:9224?x=1", "http://localhost:9224#fragment", "http://localhost:0", "http://localhost:65536"} {
		cfg := SinkConfig{Destination: &DestinationRef{Name: "work", Kind: "attach", Endpoint: endpoint}}
		if _, err := cfg.ResolveDestinations(); err == nil {
			t.Errorf("unsafe attach accepted: %s", endpoint)
		}
	}
	cfg := SinkConfig{Destinations: []DestinationRef{{Name: "personal", Kind: "attach", Endpoint: "http://localhost:09224"}, {Name: "work", Kind: "attach", Endpoint: "http://127.0.0.1:9224"}}}
	if _, err := cfg.ResolveDestinations(); err == nil {
		t.Fatal("numeric port alias accepted")
	}
}
