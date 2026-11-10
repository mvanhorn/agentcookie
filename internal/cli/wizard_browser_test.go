package cli

import (
	"bytes"
	"context"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/onboarding"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"gopkg.in/yaml.v3"
)

func TestWizardSourceBrowserYAML(t *testing.T) {
	ref := config.BrowserRef{Name: "zen", Profile: "/tmp/test profile", Reader: "auto", BiDiPort: 9225, CompanionPort: 9226, StorageReadMode: "request", Containers: []string{"Work"}, ContainerSites: map[string][]string{"Work": {"example.com"}}}
	got, err := renderSelectedSourceYAML("mini", "", ref)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.SourceConfig
	if err = yaml.Unmarshal([]byte(got), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Browser.Name != "zen" || cfg.Browser.CompanionPort != 9226 || cfg.Chrome.DBPath != "" {
		t.Fatal(got)
	}
	if cfg.Sink.URL != "http://mini:9999/sync" {
		t.Fatal(got)
	}
}
func TestWizardHeldSitesFailClosed(t *testing.T) {
	snap := &source.Snapshot{Meta: protocol.SnapshotMeta{Sites: map[string]protocol.SiteStatus{"example.com": {State: protocol.Held, Containers: []uint32{1, 2}, Reason: "Personal, Work"}}}}
	if err := checkWizardHeldSites(snap, false); err == nil || !strings.Contains(err.Error(), "Personal, Work") {
		t.Fatal(err)
	}
	if err := checkWizardHeldSites(snap, true); err != nil {
		t.Fatal(err)
	}
}
func TestWizardLiveSinkConfigNeverEnablesLegacyWrites(t *testing.T) {
	cfg := &config.SinkConfig{Listen: config.ListenRef{Addr: "100.64.0.2:9999"}, Peer: config.PeerRef{Hostname: "laptop"}}
	d := config.DestinationRef{Name: "live_chrome", Kind: "live_chrome", Required: true}
	got, err := renderDestinationSinkYAML(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	var loaded config.SinkConfig
	if err = yaml.Unmarshal([]byte(got), &loaded); err != nil {
		t.Fatal(err)
	}
	if !loaded.SkipChromeSQLite || loaded.Destination == nil || loaded.Destination.Kind != "live_chrome" || loaded.CDP.Enabled || loaded.LiveCDP.Enabled {
		t.Fatal(got)
	}
}
func TestWizardPreflightFailureStopsBeforePairingAndDaemon(t *testing.T) {
	oldDir, oldKind, oldCode, oldURL, oldPeer, oldDry, oldForce, oldPreflight := common.ConfigDir, wizardDestinationKind, wizardCode, wizardPairURL, wizardPeer, wizardDryRun, wizardForce, wizardDestinationPreflight
	defer func() {
		common.ConfigDir = oldDir
		wizardDestinationKind = oldKind
		wizardCode = oldCode
		wizardPairURL = oldURL
		wizardPeer = oldPeer
		wizardDryRun = oldDry
		wizardForce = oldForce
		wizardDestinationPreflight = oldPreflight
	}()
	home := t.TempDir()
	t.Setenv("HOME", home)
	common.ConfigDir = filepath.Join(home, "config")
	if err := os.Mkdir(common.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("listen:\n  addr: 127.0.0.1:19999\npeer:\n  hostname: laptop\ndestination:\n  name: attach\n  kind: attach\n  endpoint: http://127.0.0.1:19223\n")
	path := filepath.Join(common.ConfigDir, "sink.yaml")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	wizardDestinationKind = "attach"
	wizardCode = "fixture"
	wizardPairURL = "http://127.0.0.1:1"
	wizardPeer = "laptop"
	wizardDryRun = false
	wizardForce = false
	called := false
	wizardDestinationPreflight = func(context.Context, *config.SinkConfig) error { called = true; return context.DeadlineExceeded }
	err := wizardInstallLiveSink(context.Background(), "/nonexistent-agentcookie", filepath.Join(home, "logs"))
	if err == nil || !strings.Contains(err.Error(), "daemon not installed") {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("preflight was not invoked")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("config changed on preflight failure")
	}
	if _, err := os.Stat(filepath.Join(common.ConfigDir, "blocklist.yaml")); !os.IsNotExist(err) {
		t.Fatal("install continued after failure")
	}
}

func TestAddSinkPreservesGeckoWithoutChromePath(t *testing.T) {
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "zen", Reader: "live", BiDiPort: 9225, CompanionPort: 9226, StorageReadMode: "request", ContainerSites: map[string][]string{"Work": {"example.com"}}}}
	got := renderSourceYAMLSinks(cfg, []config.SinkTarget{{URL: "http://mini:9999/sync", Peer: "mini"}})
	if strings.Contains(got, "chrome:") || !strings.Contains(got, "container_sites:") || !strings.Contains(got, "reader: live") {
		t.Fatal(got)
	}
}
func TestExistingLiveSinkDoesNotChooseKeychainLane(t *testing.T) {
	oldDir, oldKind, oldForce := common.ConfigDir, wizardDestinationKind, wizardForce
	defer func() { common.ConfigDir = oldDir; wizardDestinationKind = oldKind; wizardForce = oldForce }()
	common.ConfigDir = t.TempDir()
	wizardDestinationKind = ""
	wizardForce = false
	if err := os.WriteFile(filepath.Join(common.ConfigDir, "sink.yaml"), []byte("listen:\n  addr: 127.0.0.1:19999\nsecurity:\n  shared_secret: synthetic-long-enough-credential-0123456789\ndestination:\n  name: test\n  kind: attach\n  endpoint: http://127.0.0.1:19223\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := selectedWizardDestination(); got != "attach" {
		t.Fatalf("selected %s instead of preserved attach", got)
	}
}
func TestSourceWizardPreviewNeverPairsOrWritesConfig(t *testing.T) {
	oldDir, oldBrowser, oldPreview, oldDry, oldForce, oldPeer := common.ConfigDir, wizardBrowser, wizardSourcePreview, wizardDryRun, wizardForce, wizardPeer
	defer func() {
		common.ConfigDir = oldDir
		wizardBrowser = oldBrowser
		wizardSourcePreview = oldPreview
		wizardDryRun = oldDry
		wizardForce = oldForce
		wizardPeer = oldPeer
		wizardSelectedSourceYAML = ""
	}()
	home := t.TempDir()
	t.Setenv("HOME", home)
	common.ConfigDir = filepath.Join(home, "not-created")
	wizardBrowser = config.BrowserRef{Name: "chrome", Reader: "auto"}
	wizardDryRun = true
	wizardForce = false
	wizardPeer = "mini"
	called := false
	wizardSourcePreview = func(_ context.Context, cfg *config.SourceConfig) (*source.Snapshot, error) {
		called = true
		if cfg.Browser.Name != "chrome" {
			t.Fatal(cfg)
		}
		return &source.Snapshot{}, nil
	}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := prepareWizardBrowser(cmd); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("dry-read not invoked")
	}
	if _, err := os.Stat(common.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("config directory created before install")
	}
}
func TestHeldContainerNamesComeFromCapture(t *testing.T) {
	snap := &source.Snapshot{RoutingData: &geckoreader.Capture{Identities: []gecko.Identity{{Name: "Work", CookieStoreID: "firefox-container-2"}}}, Meta: protocol.SnapshotMeta{Sites: map[string]protocol.SiteStatus{"example.com": {State: protocol.Held, Containers: []uint32{0, 2}}}}}
	err := checkWizardHeldSites(snap, false)
	if err == nil || !strings.Contains(err.Error(), "Work (2)") || !strings.Contains(err.Error(), "Default (0)") {
		t.Fatal(err)
	}
}
func TestWizardContainerSitesRouteAndReassign(t *testing.T) {
	overrides := map[string][]string{}
	for _, entry := range []string{"github.com=Work", "example.com=Personal", "https://api.github.com/project=Personal"} {
		if err := setWizardContainerSite(overrides, entry); err != nil {
			t.Fatal(err)
		}
	}
	identities := []gecko.Identity{{Name: "Work", CookieStoreID: "firefox-container-1"}, {Name: "Personal", CookieStoreID: "firefox-container-2"}}
	records := []gecko.Record{{Site: "github.com", Container: 1}, {Site: "github.com", Container: 2}, {Site: "example.com", Container: 1}, {Site: "example.com", Container: 2}}
	routes, err := gecko.Route(records, nil, identities, nil, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if routes["github.com"].State != protocol.Shipped || routes["github.com"].Container != 2 || routes["example.com"].Container != 2 {
		t.Fatalf("routes=%+v overrides=%+v", routes, overrides)
	}
	if _, ok := overrides["Work"]; ok {
		t.Fatalf("stale assignment: %+v", overrides)
	}
	// The same container can carry several sites, and repeated choices are idempotent.
	if err := setWizardContainerSite(overrides, "github.com=Personal"); err != nil {
		t.Fatal(err)
	}
	if len(overrides["Personal"]) != 2 {
		t.Fatalf("duplicate site: %+v", overrides)
	}
}
func TestWizardLauncherHintQuotesShellMetacharacters(t *testing.T) {
	got := wizardGeckoLauncherCommand("/tmp/config;other", config.BrowserRef{Name: "zen", BiDiPort: 9225, AppPath: "/tmp/Zen's.app"}, "/tmp/$(false)")
	for _, want := range []string{"--config-dir '/tmp/config;other'", "--profile '/tmp/$(false)'", "--bidi-port 9225", "--app-path '/tmp/Zen'\"'\"'s.app'"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
func TestExistingLegacyLiveCDPSinkKeepsAttachLane(t *testing.T) {
	oldDir, oldKind, oldForce, oldWrite := common.ConfigDir, wizardDestinationKind, wizardForce, wizardWriteChromeSQLite
	defer func() {
		common.ConfigDir = oldDir
		wizardDestinationKind = oldKind
		wizardForce = oldForce
		wizardWriteChromeSQLite = oldWrite
	}()
	common.ConfigDir = t.TempDir()
	wizardDestinationKind = ""
	wizardForce = false
	wizardWriteChromeSQLite = false
	if err := os.WriteFile(filepath.Join(common.ConfigDir, "sink.yaml"), []byte("listen:\n  addr: 127.0.0.1:19999\npeer:\n  hostname: laptop\nlive_cdp:\n  enabled: true\n  endpoint: http://127.0.0.1:19223\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := selectedWizardDestination(); got != "attach" {
		t.Fatalf("selected %s instead of existing CDP attach", got)
	}
}
func TestWizardInstalledBrowserListUsesRegistryAndExecutableProbe(t *testing.T) {
	got := wizardInstalledBrowserNames([]string{"chrome", "zen", "firefox"}, func(path string) bool { return path == "/Applications/Zen.app" }, func(name string) bool { return name == "firefox" })
	if strings.Join(got, ",") != "firefox,zen" {
		t.Fatal(got)
	}
}

func TestGeckoWizardStoragePreviewFollowsConsent(t *testing.T) {
	oldDir, oldBrowser, oldPreview, oldAllow, oldDry, oldForce, oldPeer := common.ConfigDir, wizardBrowser, wizardSourcePreview, wizardAllowCompanion, wizardDryRun, wizardForce, wizardPeer
	defer func() {
		common.ConfigDir = oldDir
		wizardBrowser = oldBrowser
		wizardSourcePreview = oldPreview
		wizardAllowCompanion = oldAllow
		wizardDryRun = oldDry
		wizardForce = oldForce
		wizardPeer = oldPeer
		wizardSelectedSourceYAML = ""
	}()
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, "fixture-profile")
	if err := os.MkdirAll(filepath.Join(profile, "storage", "default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "times.json"), []byte(`{"created":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	common.ConfigDir = filepath.Join(home, "not-created")
	wizardBrowser = config.BrowserRef{Name: "firefox", Profile: profile, Reader: "auto", StorageReadMode: "request", BiDiPort: 19325, CompanionPort: 19326}
	wizardDryRun = true
	wizardForce = false
	wizardPeer = "fixture-sink"
	wizardAllowCompanion = false
	calls := 0
	wizardSourcePreview = func(ctx context.Context, cfg *config.SourceConfig) (*source.Snapshot, error) {
		if cfg.Browser.StorageReadMode != "request" {
			t.Fatal("explicit request mode lost")
		}
		calls++
		scope := geckoReadScope(ctx)
		if !scope.LocalStorage || !scope.IndexedDB {
			t.Fatal("consented wizard preview missing storage scope")
		}
		return &source.Snapshot{}, nil
	}
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var notice bytes.Buffer
	cmd.SetErr(&notice)
	cmd.SetIn(strings.NewReader("n\n"))
	if err := prepareWizardBrowser(cmd); err == nil {
		t.Fatalf("missing explicit consent: %v", err)
	}
	if calls != 0 {
		t.Fatal("preview before consent")
	}
	if !strings.Contains(notice.String(), "cookie-bearing GET") || !strings.Contains(notice.String(), "server-side activity") {
		t.Fatal("request-mode navigation disclosure missing")
	}
	wizardAllowCompanion = true
	if err := prepareWizardBrowser(cmd); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("preview calls %d", calls)
	}
	if _, err := os.Stat(common.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("preview wrote config")
	}
}

func TestGeckoStorageReadModeDefaultsAgree(t *testing.T) {
	flag := wizardInstallCmd.Flags().Lookup("storage-read-mode")
	if flag == nil || flag.DefValue != "intercept" {
		t.Fatalf("wizard default: %+v", flag)
	}
	for _, mode := range []string{"", "request"} {
		t.Run("mode="+mode, func(t *testing.T) {
			expected := mode
			if expected == "" {
				expected = "intercept"
			}
			plan, err := onboarding.SourcePlan(onboarding.SourceInput{Browser: "zen", StorageReadMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			var planned config.SourceConfig
			if err = yaml.Unmarshal([]byte(plan), &planned); err != nil {
				t.Fatal(err)
			}
			if planned.Browser.StorageReadMode != expected {
				t.Fatalf("planner mode=%q want %q", planned.Browser.StorageReadMode, expected)
			}
			dir := t.TempDir()
			raw := "browser:\n  name: zen\n"
			if mode != "" {
				raw += "  storage_read_mode: " + mode + "\n"
			}
			if err = os.WriteFile(filepath.Join(dir, "source.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.LoadSourceLocalWithOverrides(dir, config.SourceOverrides{})
			if err != nil {
				t.Fatal(err)
			}
			effective, err := config.EffectiveSource(&config.SourceConfig{Browser: config.BrowserRef{Name: "zen", StorageReadMode: mode}}, config.SourceOverrides{})
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Browser.StorageReadMode != expected || effective.Browser.StorageReadMode != expected {
				t.Fatalf("config/wizard input defaults differ: loaded=%q effective=%q", loaded.Browser.StorageReadMode, effective.Browser.StorageReadMode)
			}
		})
	}
}
