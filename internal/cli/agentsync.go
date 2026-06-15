package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
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
	// LoadSourceLocal: the agent-sync loop has no push target, so it must not
	// require sink.url or a peer/secret. Missing source.yaml is fine (default
	// Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	if _, err := loadFreshBlocklist(); err != nil {
		return err
	}

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
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}
	skipDBSC := agentSyncSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"
	domainFilter := agentSyncDomains

	// Cookie provider: read+decrypt+filter fresh each call so the loop always
	// injects current values.
	provider := func() ([]chrome.Cookie, error) {
		blocklist, err := loadFreshBlocklist()
		if err != nil {
			return nil, err
		}
		cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		cookies = sinkpush.FilterByHostPatterns(cookies, domainFilter)
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: read %d, filtered %d, dbsc(warn=%d skip=%d), injecting %d\n",
				st.totalRead, st.totalDropped, st.dbsc.warned, st.dbsc.skipped, len(cookies))
		}
		return cookies, nil
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

	oc, err := livecdp.LaunchOwnedChrome(ctx, agentSyncChromePath, userDataDir, agentSyncPort, !agentSyncHeaded)
	if err != nil {
		return err
	}
	defer oc.Close()

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, oc.Endpoint)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	syncLog := func(format string, a ...any) {
		if agentSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie agent-sync: "+format+"\n", a...)
		}
	}
	syncer := livecdp.NewSyncer(browserCtx, provider, syncLog)

	// Initial inject so the owned browser's default context is logged in
	// immediately; also surfaces connection/cookie errors at startup.
	n, err := syncer.ReinjectAll()
	if err != nil {
		return fmt.Errorf("agent-sync: initial inject: %w", err)
	}

	fmt.Fprintf(os.Stderr, "agentcookie agent-sync: owned Chrome on %s (profile %s); injected %d context(s)\n", oc.Endpoint, userDataDir, n)
	fmt.Fprintln(os.Stderr, "Connect an agent browser:")
	fmt.Fprintf(os.Stderr, "  browser-use --cdp-url %s open https://github.com\n", oc.Endpoint)
	fmt.Fprintf(os.Stderr, "  agent-browser --cdp %d\n", oc.Port)
	fmt.Fprintln(os.Stderr, "Watching Chrome cookies + new contexts. Ctrl-C to stop.")

	// Poll for new contexts (e.g. the one browser-use creates on connect) and
	// inject them. Runs concurrently with the cookie-change watcher below.
	go func() {
		if runErr := syncer.Run(ctx); runErr != nil && runErr != context.Canceled {
			syncLog("context poll: %v", runErr)
		}
	}()

	// Watch the source Chrome cookie DB; on each debounced change, re-inject
	// current cookies into every live context so a site the user just logged
	// into in their real Chrome becomes logged-in in the agent browser too.
	// A failed cycle is logged and the watcher keeps running.
	w, err := watcher.New(watcher.Config{
		CookiesPath: cfg.Chrome.DBPath,
		LogLabel:    "agentcookie agent-sync",
		Push: func(context.Context) (int, error) {
			return syncer.ReinjectAll()
		},
		OnEvent: func(ev watcher.Event) {
			if agentSyncVerbose {
				fmt.Fprintf(os.Stderr, "agentcookie agent-sync: %s\n", ev.String())
			}
		},
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	err = w.Run(ctx)
	if err != nil && err != context.Canceled {
		return err
	}
	fmt.Fprintln(os.Stderr, "agentcookie agent-sync: stopped")
	return nil
}
