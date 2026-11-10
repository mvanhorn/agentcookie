package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/source"
)

var (
	agentSyncProfile     string
	agentSyncReader      string
	agentSyncPort        int
	agentSyncHeaded      bool
	agentSyncChromePath  string
	agentSyncUserDataDir string
	agentSyncSkipDBSC    bool
	agentSyncDomains     []string
	agentSyncBrowser     string
	agentSyncVerbose     bool
)

var agentSyncCmd = &cobra.Command{
	Use:   "agent-sync",
	Short: "Run an owned Chrome that Chromium agent browsers connect to, kept logged in from your real Chrome",
	Long: `agent-sync is the Chromium counterpart to cmux-sync. It launches a
dedicated Chrome on a loopback debug port, reads this Mac's Chrome cookies
(decrypt + cookie policy + DBSC filter, the same pipeline source uses), and
injects them -- as plaintext, over CDP -- into every browser context that
Chrome opens, including the context a connector like browser-use creates for
itself. browser-use / agent-browser connect to it via --cdp-url and wake up
logged into your sites.

This is live injection, not a cold profile or a storage_state file: cookies
go straight into the running browser's in-memory store, so Chrome 127+
App-Bound Encryption (which makes cold-profile cookies undecryptable on load)
never applies. The owned Chrome uses its own user-data-dir, so the debug port
is honored (Chrome 136+ only blocks it on the default profile) and your
everyday Chrome is never touched.

  agentcookie agent-sync                      launch + sync, hold until Ctrl-C
  agentcookie agent-sync --headed             show the owned browser window
  agentcookie agent-sync --domain %github.com limit to matching hosts

Device-bound (DBSC) cookies -- Google/Workspace account cookies -- cannot
transfer to another browser and are reported, not faked. Non-DBSC sites
(GitHub-class, the large majority) work.`,
	RunE: runAgentSync,
}

func init() {
	agentSyncCmd.Flags().StringVar(&agentSyncProfile, "profile", "", "source profile selector")
	agentSyncCmd.Flags().StringVar(&agentSyncReader, "reader", "", "source reader: auto or file")
	agentSyncCmd.Flags().IntVar(&agentSyncPort, "port", 9400, "loopback Chrome remote-debugging port for the owned browser")
	agentSyncCmd.Flags().BoolVar(&agentSyncHeaded, "headed", false, "show the owned browser window (default: headless)")
	agentSyncCmd.Flags().StringVar(&agentSyncChromePath, "chrome-path", "", "override the Chrome executable (default: auto-detect)")
	agentSyncCmd.Flags().StringVar(&agentSyncUserDataDir, "user-data-dir", "", "owned-browser profile dir (default: ~/.agentcookie/agent-chrome)")
	agentSyncCmd.Flags().BoolVar(&agentSyncSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	agentSyncCmd.Flags().StringSliceVar(&agentSyncDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	agentSyncCmd.Flags().StringVar(&agentSyncBrowser, "browser", "", "source browser name (default: source.yaml browser, then Chrome)")
	agentSyncCmd.Flags().BoolVar(&agentSyncVerbose, "verbose", false, "log per-cycle counts to stderr")
}

func runAgentSync(cmd *cobra.Command, args []string) error {
	ctx, closeReaders := withGeckoSession(cmd.Context())
	defer closeReaders()
	if cmd.Flags().Changed("profile") {
		ctx = context.WithValue(ctx, geckoExplicitProfileKey{}, true)
	}
	cmd.SetContext(ctx)
	// LoadSourceLocal: the agent-sync loop has no push target, so it must not
	// require sink.url or a peer/secret. Missing source.yaml is fine (default
	// Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocalWithOverrides(common.ConfigDir, config.SourceOverrides{Browser: agentSyncBrowser, Profile: agentSyncProfile, Reader: agentSyncReader})
	if err != nil {
		return err
	}
	if _, err := loadFreshBlocklist(); err != nil {
		return err
	}

	var key []byte
	if !cfg.CDPSource.Enabled && !isGeckoSource(cfg) {
		browserName := agentSyncBrowser
		if browserName == "" {
			browserName = cfg.Browser.Name
		}
		sourceBrowser, err := chrome.LookupBrowser(browserName)
		if err != nil {
			return err
		}
		password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
		if err != nil {
			return err
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
	}
	skipDBSC := agentSyncSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"
	domainFilter := agentSyncDomains

	// Cookie provider: read+decrypt+filter fresh each call so the loop always
	// injects current values.
	provider := func() (*source.Snapshot, error) {
		blocklist, err := loadFreshBlocklist()
		if err != nil {
			return nil, err
		}
		snap, err := readConfiguredSnapshot(ctx, cfg, blocklist, key, skipDBSC, time.Now().UTC(), true)
		if err != nil {
			return nil, err
		}
		snap.Cookies = sinkpush.FilterByHostPatterns(snap.Cookies, domainFilter)
		return snap, nil
	}

	userDataDir := agentSyncUserDataDir
	if userDataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		userDataDir = filepath.Join(home, ".agentcookie", "agent-chrome")
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	headed := agentSyncHeaded
	owned := destination.NewManaged(config.DestinationRef{Name: "agent-sync", Kind: "live_chrome", Required: true, Chrome: config.DestinationChrome{UserDataDir: userDataDir, Port: agentSyncPort, Headed: &headed, Start: "on_demand", Relaunch: "after_crash", Path: agentSyncChromePath}})
	defer owned.Close()
	applier, _, err := newDestinationApplier(owned)
	if err != nil {
		return err
	}
	var master []byte
	if keystore.MasterKeyExists() {
		master, err = keystore.ReadMasterKey()
		if err != nil {
			return err
		}
	}
	engine, err := destination.Open(filepath.Join(home, ".agentcookie", "destinations", "agent-sync"), master, applier)
	if err != nil {
		return err
	}
	counters := source.Counters{Dir: filepath.Join(home, ".agentcookie")}
	syncOnce := func() error {
		snap, err := provider()
		if err != nil {
			return err
		}
		g, err := counters.NextGeneration()
		if err != nil {
			return err
		}
		if prior, ok := engine.Status("local"); ok && g <= prior.Accepted {
			g, err = counters.ReseedGeneration(prior.Accepted + 1)
			if err != nil {
				return err
			}
		}
		snap.Meta.Generation = g
		env := protocol.SyncEnvelope{ProtocolVersion: 3, SourceHostname: "local", Snapshot: &snap.Meta, Cookies: snap.Cookies, Storage: snap.Storage}
		desired, _, err := convertDestinationSnapshot(env)
		if err != nil {
			return err
		}
		status, err := engine.Accept(ctx, "local", desired)
		if err != nil {
			return err
		}
		if !status.Ready {
			return fmt.Errorf("destination pending: %v", status.Failed)
		}
		return nil
	}
	if err = syncOnce(); err != nil {
		return fmt.Errorf("agent-sync initial restore: %w", err)
	}
	if agentSyncHeaded {
		if err = owned.OpenWindow(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "agentcookie agent-sync: restored owned Chrome at %s; source logout removes only owned records\n", owned.Chrome.Endpoint)
	go func() { _ = owned.Run(ctx, engine) }()
	// A bounded baseline tick works for file, CDP and Gecko live readers. Each
	// read revalidates its own running-browser preconditions and never falls back.
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-tick.C:
			if err := syncOnce(); err != nil {
				fmt.Fprintf(os.Stderr, "agentcookie agent-sync: %v\n", err)
			}
		}
	}

	fmt.Fprintln(os.Stderr, "agentcookie agent-sync: stopped")
	return nil
}
