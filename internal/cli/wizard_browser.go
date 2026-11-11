package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/onboarding"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var wizardBrowser config.BrowserRef
var wizardContainerSites []string
var wizardAcceptHeld, wizardAllowCompanion, wizardDryRun bool
var wizardDestinationKind, wizardDestinationProfile, wizardDestinationEndpoint, wizardChromePath string
var wizardDestinationPort int
var wizardSelectedSourceYAML string

func init() {
	f := wizardInstallCmd.Flags()
	f.StringVar(&wizardBrowser.Name, "browser", "", "[source] browser you log in with (required; prompted on a terminal)")
	f.StringVar(&wizardBrowser.Profile, "profile", "", "[source] explicit profile selector; otherwise show install-aware default")
	f.StringVar(&wizardBrowser.Reader, "reader", "auto", "[source] auto, live or file")
	f.StringVar(&wizardBrowser.AppPath, "app-path", "", "[source] Gecko app bundle or executable")
	f.IntVar(&wizardBrowser.BiDiPort, "bidi-port", 9225, "[source] loopback BiDi port (separate from owned Chrome)")
	f.IntVar(&wizardBrowser.CompanionPort, "companion-port", 9226, "[source] loopback authenticated companion port")
	f.StringVar(&wizardBrowser.StorageReadMode, "storage-read-mode", "intercept", "[source] intercept (default) or explicit request (cookie-bearing GETs)")
	f.StringSliceVar(&wizardBrowser.Containers, "containers", nil, "[source] preferred container names/IDs")
	f.StringArrayVar(&wizardContainerSites, "container-sites", nil, "[source] site=container override; repeat for multiple selections")
	f.BoolVar(&wizardAcceptHeld, "accept-held-sites", false, "[source] accept holding unresolved sites without deleting destination state")
	f.BoolVar(&wizardAllowCompanion, "allow-companion", false, "[source] allow temporary companion with broad cookie/site permissions and BiDi broker")
	f.BoolVar(&wizardDryRun, "dry-run", false, "preview and dry-read only; runtime state may update, but no pairing, config or service changes")
	f.StringVar(&wizardDestinationKind, "destination", "", "[sink] live_chrome, attach or legacy ppcli; defaults by platform for new installs")
	f.StringVar(&wizardDestinationProfile, "destination-profile", "", "[sink] dedicated owned Chrome profile directory")
	f.IntVar(&wizardDestinationPort, "destination-port", 9224, "[sink] owned Chrome loopback CDP port")
	f.StringVar(&wizardDestinationEndpoint, "destination-endpoint", "http://127.0.0.1:9223", "[sink] attach CDP endpoint")
	f.StringVar(&wizardChromePath, "chrome-path", "", "[sink] owned Chrome executable")
}
func wizardInteractive() bool {
	st, err := os.Stdin.Stat()
	return err == nil && (st.Mode()&os.ModeCharDevice) != 0
}
func wizardAsk(cmd *cobra.Command, prompt string) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), prompt)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	return strings.TrimSpace(line), err
}

func prepareWizardBrowser(cmd *cobra.Command) error {
	if wizardBrowser.Name == "" {
		if !wizardInteractive() {
			return fmt.Errorf("which browser do you log in with? --browser is required")
		}
		names := append(browsers.SupportedNames(browsers.Chromium), browsers.SupportedNames(browsers.Gecko)...)
		installed := wizardInstalledBrowserNames(names, func(path string) bool { _, err := os.Stat(path); return err == nil }, func(name string) bool { _, err := exec.LookPath(name); return err == nil })
		if len(installed) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "Detected installed browsers: %s\n", strings.Join(installed, ", "))
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Supported source browsers (custom installs may not be detected): %s\n", strings.Join(names, ", "))
		name, err := wizardAsk(cmd, "Which browser do you log in with? ")
		if err != nil {
			return err
		}
		wizardBrowser.Name = name
	}
	b, err := browsers.Lookup(wizardBrowser.Name)
	if err != nil {
		return err
	}
	wizardBrowser.Name = b.Name
	wizardBrowser.ContainerSites = map[string][]string{}
	for _, entry := range wizardContainerSites {
		if err := setWizardContainerSite(wizardBrowser.ContainerSites, entry); err != nil {
			return err
		}
	}

	// Existing config is authoritative unless replacement was explicitly chosen.
	path := filepath.Join(common.ConfigDir, "source.yaml")
	cfg := &config.SourceConfig{Browser: wizardBrowser}
	if fileExists(path) && !wizardForce {
		existing, err := config.LoadSourceLocalWithOverrides(common.ConfigDir, config.SourceOverrides{})
		if err != nil {
			return err
		}
		if existing.Browser.Name != wizardBrowser.Name {
			return fmt.Errorf("existing source uses %s; review and pass --force to replace it", existing.Browser.Name)
		}
		for _, flag := range []string{"profile", "reader", "app-path", "bidi-port", "companion-port", "storage-read-mode", "containers", "container-sites"} {
			if cmd.Flags().Changed(flag) {
				return fmt.Errorf("existing source configuration is preserved; use --force after reviewing changes to --%s", flag)
			}
		}
		cfg = existing
	} else {
		cfg, err = config.EffectiveSource(cfg, config.SourceOverrides{})
		if err != nil {
			return err
		}
	}
	if b.Family == browsers.Gecko {
		identity, err := resolveGeckoProfile(cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Profile: %s (created %d); How: %s; install hash: %s\n", identity.Dir, identity.Created, identity.How, identity.InstallHash)
		fmt.Fprintln(cmd.ErrOrStderr(), "Engine-bound sessions can require a fresh login after switching from Gecko to Chrome.")
		fmt.Fprintln(cmd.ErrOrStderr(), "Companion permissions: cookies, contextualIdentities, tabs, tabHide, webNavigation, scripting, <all_urls>. BiDi broker controls the selected profile through loopback. The browser may show a one-time hiding-tabs notice.")
		if cfg.Browser.StorageReadMode == "request" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Request storage mode sends a cookie-bearing GET per origin per read; it can trigger server-side activity.")
		}
		if cfg.Browser.Reader != "file" && !wizardAllowCompanion {
			if !wizardInteractive() {
				return fmt.Errorf("review companion permissions, then pass --allow-companion or choose --reader file")
			}
			answer, err := wizardAsk(cmd, "Allow the temporary companion and broker? [y/N] ")
			if err != nil {
				return err
			}
			if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
				return fmt.Errorf("companion not authorized")
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Optional launcher (does not start the browser): %s\n", wizardGeckoLauncherCommand(common.ConfigDir, cfg.Browser, identity.Dir))
	}
	if b.Family == browsers.Chromium {
		fmt.Fprintf(cmd.ErrOrStderr(), "Profile: %s; How: selected Chromium profile; cookies path: %s\n", cfg.Browser.Profile, cfg.Chrome.DBPath)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if b.Family == browsers.Gecko && cfg.Browser.Reader != "file" {
		// This preview follows the explicit companion/navigation notice and consent.
		ctx = withGeckoStoragePreview(ctx, true, true)
	}
	if cmd.Flags().Changed("profile") {
		ctx = context.WithValue(ctx, geckoExplicitProfileKey{}, true)
	}
	snap, err := wizardSourcePreview(ctx, cfg)
	if err != nil {
		return fmt.Errorf("source dry read: %w", err)
	}
	printWizardSnapshot(cmd, snap)
	for checkWizardHeldSites(snap, wizardAcceptHeld) != nil {
		if !wizardInteractive() {
			return checkWizardHeldSites(snap, false)
		}
		answer, e := wizardAsk(cmd, "Choose site=container, type hold to accept holding, or press Enter to stop: ")
		if e != nil {
			return e
		}
		if strings.EqualFold(answer, "hold") {
			break
		}
		if fileExists(path) && !wizardForce {
			return fmt.Errorf("changing container routing requires --force for the existing source config; review and rerun with --container-sites %q", answer)
		}
		if cfg.Browser.ContainerSites == nil {
			cfg.Browser.ContainerSites = map[string][]string{}
		}
		if err := setWizardContainerSite(cfg.Browser.ContainerSites, answer); err != nil {
			return err
		}

		snap, err = wizardSourcePreview(ctx, cfg)
		if err != nil {
			return fmt.Errorf("source dry read: %w", err)
		}
		printWizardSnapshot(cmd, snap)
	}
	wizardSelectedSourceYAML, err = renderSelectedSourceYAML(wizardPeer, wizardSinkURL, cfg.Browser)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Effective source: %s / %s -> %s (destination negotiated with sink)\n", cfg.Browser.Name, cfg.Browser.Profile, wizardPeer)
	if wizardDryRun {
		fmt.Fprint(cmd.OutOrStdout(), wizardSelectedSourceYAML)
	}
	return nil
}

var wizardSourcePreview = func(ctx context.Context, cfg *config.SourceConfig) (*source.Snapshot, error) {
	policy, err := config.LoadBlocklist(common.ConfigDir)
	if err != nil {
		return nil, err
	}
	if isGeckoSource(cfg) {
		return readGeckoSnapshot(ctx, cfg, policy)
	}
	r, err := source.New(cfg, source.Options{}, source.Dependencies{Password: chrome.SafeStoragePasswordFor})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Read(ctx, policy)
}

func checkWizardHeldSites(snap *source.Snapshot, accept bool) error {
	if snap == nil {
		return fmt.Errorf("source returned no snapshot")
	}
	var held []string
	for site, s := range snap.Meta.Sites {
		if s.State == protocol.Held {
			held = append(held, fmt.Sprintf("%s: containers %v (%s)", site, wizardContainerNames(snap, s.Containers), s.Reason))
		}
	}
	sort.Strings(held)
	if len(held) > 0 && !accept {
		return fmt.Errorf("held sites: %s; select --containers/--container-sites or --accept-held-sites", strings.Join(held, "; "))
	}
	return nil
}
func printWizardSnapshot(cmd *cobra.Command, snap *source.Snapshot) {
	if snap == nil {
		return
	}
	origins, ls, idb := 0, 0, 0
	if snap.Storage != nil {
		origins = len(snap.Storage.Origins)
		for _, o := range snap.Storage.Origins {
			ls += len(o.LocalStorage)
			for _, db := range o.IndexedDB {
				for _, s := range db.Stores {
					idb += len(s.Records)
				}
			}
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Reader: %s (%s); catalog listed=%t unread=%d unreadable=%d; cookies=%d storage origins=%d LS=%d IDB records=%d\n", snap.ReaderMode, snap.ReaderReason, snap.Meta.Inventory.Listed, snap.Meta.Inventory.Unread, snap.Meta.Inventory.Unreadable, len(snap.Cookies), origins, ls, idb)
	if capture, ok := snap.RoutingData.(*geckoreader.Capture); ok {
		fmt.Fprintf(cmd.ErrOrStderr(), "Catalog scoped origins: %d\n", len(capture.Origins))
		if capture.Profile.Dir != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "Read profile: %s; How: %s; install hash: %s\n", capture.Profile.Dir, capture.Profile.How, capture.Profile.InstallHash)
		}
	}
	sites := make([]string, 0, len(snap.Meta.Sites))
	for site := range snap.Meta.Sites {
		sites = append(sites, site)
	}
	sort.Strings(sites)
	for _, site := range sites {
		s := snap.Meta.Sites[site]
		if s.State == protocol.Held {
			fmt.Fprintf(cmd.ErrOrStderr(), "Held %s: containers %v; %s\n", site, wizardContainerNames(snap, s.Containers), s.Reason)
		}
	}
}
func renderSelectedSourceYAML(peer, sinkURL string, ref config.BrowserRef) (string, error) {
	if sinkURL == "" {
		sinkURL = "http://" + peer + ":9999/sync"
	}
	// Marshal typed values, not string interpolation of user-controlled YAML.
	data, err := yaml.Marshal(struct {
		Browser config.BrowserRef `yaml:"browser"`
		Sink    config.SinkRef    `yaml:"sink"`
		Peer    config.PeerRef    `yaml:"peer"`
	}{ref, config.SinkRef{URL: sinkURL}, config.PeerRef{Hostname: peer}})
	return string(data), err
}
func selectedWizardDestination() string {
	if wizardDestinationKind != "" {
		return wizardDestinationKind
	}
	if wizardWriteChromeSQLite {
		return "ppcli"
	}
	if fileExists(filepath.Join(common.ConfigDir, "sink.yaml")) && !wizardForce {
		if cfg, err := config.LoadSink(common.ConfigDir); err == nil && (cfg.HasDestinations() || cfg.LiveCDP.Enabled) {
			if ds, err := cfg.ResolveDestinations(); err == nil && len(ds) > 0 {
				return ds[0].Kind
			}
		}
		return "ppcli"
	} // preserve existing legacy installations
	return onboarding.DefaultDestination(runtime.GOOS, wizardGUISession())
}
func renderDestinationSinkYAML(cfg *config.SinkConfig, d config.DestinationRef) (string, error) {
	out := *cfg
	out.Destination = &d
	out.Destinations = nil
	out.SkipChromeSQLite = true
	out.CDP = config.CDPRef{}
	out.LiveCDP = config.LiveCDPRef{}
	out.Chrome = config.ChromeRef{}
	out.Delivery = ""
	data, err := yaml.Marshal(out)
	return string(data), err
}

var wizardDestinationPreflight = func(ctx context.Context, cfg *config.SinkConfig) error {
	p, err := newDestinationPipeline(cfg)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("live destination pipeline unavailable")
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for _, name := range p.order {
		entry := p.entries[name]
		err = onboarding.ReadyBeforeInstall(ctx, onboarding.ReadyHooks{
			Prepare: entry.managed.Ensure,
			Restore: func(ctx context.Context) (bool, error) {
				contexts, err := entry.managed.Contexts(ctx)
				if err != nil {
					return false, err
				}
				if len(contexts) == 0 {
					return false, fmt.Errorf("destination has no browser context")
				}
				for _, s := range entry.engine.Sources() {
					status, err := entry.engine.Reconcile(ctx, s)
					if err != nil {
						return false, err
					}
					if !status.Ready {
						return false, fmt.Errorf("destination %s not ready: pending=%v failed=%v repair=%v", name, status.Pending, status.Failed, status.RepairNeeded)
					}
				}
				return true, nil
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func wizardSourceYAML() string {
	if wizardSelectedSourceYAML != "" {
		return wizardSelectedSourceYAML
	}
	return renderSourceYAML(wizardPeer, wizardSinkURL)
}

// Detect an actual macOS GUI bootstrap session, not whether stdin has a TTY.
var wizardGUISession = func() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/bin/launchctl", "print", fmt.Sprintf("gui/%d", os.Getuid())).Run() == nil
}

func wizardContainerNames(snap *source.Snapshot, ids []uint32) []string {
	names := map[uint32]string{0: "Default"}
	if capture, ok := snap.RoutingData.(*geckoreader.Capture); ok {
		for _, identity := range capture.Identities {
			if id, err := gecko.ContainerID(identity.CookieStoreID); err == nil {
				names[id] = identity.Name
			}
		}
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name := names[id]
		if name == "" {
			name = "unknown/deleted container"
		}
		out = append(out, fmt.Sprintf("%s (%d)", name, id))
	}
	return out
}

// The CLI is site=container; the config contract is container -> []sites.
// Reassignment removes normalized aliases so one site cannot select two accounts.
func setWizardContainerSite(overrides map[string][]string, entry string) error {
	raw, container, ok := strings.Cut(entry, "=")
	container = strings.TrimSpace(container)
	if !ok || container == "" {
		return fmt.Errorf("--container-sites requires site=container")
	}
	canonical := func(raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || u.User != nil {
			return "", fmt.Errorf("invalid container site")
		}
		return gecko.Site(strings.TrimSuffix(u.Hostname(), ".")), nil
	}
	site, err := canonical(raw)
	if err != nil {
		return err
	}
	for name, sites := range overrides {
		retained := make([]string, 0, len(sites))
		for _, existing := range sites {
			normalized, e := canonical(existing)
			if e != nil {
				return e
			}
			if normalized != site {
				retained = append(retained, existing)
			}
		}
		if len(retained) == 0 {
			delete(overrides, name)
		} else {
			overrides[name] = retained
		}
	}
	overrides[container] = append(overrides[container], site)
	return nil
}

func wizardGeckoLauncherCommand(configDir string, browser config.BrowserRef, profile string) string {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := "agentcookie --config-dir " + quote(configDir) + " gecko-launcher install --browser " + quote(browser.Name) + " --profile " + quote(profile) + " --bidi-port " + fmt.Sprint(browser.BiDiPort)
	if browser.AppPath != "" {
		command += " --app-path " + quote(browser.AppPath)
	}
	return command
}

func wizardInstalledBrowserNames(names []string, exists func(string) bool, onPath func(string) bool) []string {
	var installed []string
	for _, name := range names {
		b, err := browsers.Lookup(name)
		if err != nil {
			continue
		}
		if b.AppBundle != "" && exists(b.AppBundle) || onPath(b.Name) {
			installed = append(installed, b.Name)
		}
	}
	sort.Strings(installed)
	return installed
}
