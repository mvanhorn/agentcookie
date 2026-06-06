package cli

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
	orcaSyncOnce     bool
	orcaSyncWatch    bool
	orcaSyncVerbose  bool
	orcaSyncDryRun   bool
	orcaSyncSkipDBSC bool
	orcaSyncDomains  []string
	orcaSyncCDP      string
	orcaSyncBrowser  string
)

// orcaSyncPollInterval is how often --watch re-injects the current cookie set
// into orca's open panes, independent of Chrome cookie changes. It catches a
// browser pane the user opens after the watcher started: the fsnotify watch
// only fires on a Chrome cookie write, so without a poll a freshly-opened pane
// would read logged-out until the next login change.
const orcaSyncPollInterval = 5 * time.Second

var orcaSyncCmd = &cobra.Command{
	Use:   "orca-sync",
	Short: "Local loop: read this machine's Chrome and inject the session into orca's browser over CDP",
	Long: `orca-sync is the Electron counterpart to cmux-sync and agent-sync. It
reads this Mac's Chrome cookies (decrypt + blocklist + DBSC filter, the same
pipeline source uses) and injects them -- as plaintext, over the DevTools
Protocol -- into orca's embedded browser panes, so a browser pane in orca
wakes up logged into your sites. No orca change: orca is Electron, so it
exposes CDP when launched with --remote-debugging-port.

  agentcookie orca-sync --once    one read+inject cycle, then exit.
  agentcookie orca-sync --watch   long-running; fsnotify watches Chrome's
                                  Cookies SQLite and re-injects on change, and
                                  re-injects open panes periodically so a pane
                                  opened later also wakes up logged in.

Setup: launch orca with a loopback debug port, e.g.
  open -a Orca --args --remote-debugging-port=9222
and open a browser pane in orca (orca-sync injects into open panes). Note the
real tradeoff: while that port is open, any local process can drive orca's
browser, so prefer launching it intentionally.

Device-bound (DBSC) cookies -- Google/Workspace account cookies -- cannot
transfer to another browser and are reported, not faked. Non-DBSC sites
(GitHub-class, the large majority) work.`,
	RunE: runOrcaSync,
}

func init() {
	orcaSyncCmd.Flags().BoolVar(&orcaSyncOnce, "once", false, "single read+inject cycle, then exit")
	orcaSyncCmd.Flags().BoolVar(&orcaSyncWatch, "watch", false, "long-running fsnotify watcher; re-injects on every Chrome cookie write (debounced) and polls open panes")
	orcaSyncCmd.Flags().BoolVar(&orcaSyncVerbose, "verbose", false, "log per-cycle counts to stderr")
	orcaSyncCmd.Flags().BoolVar(&orcaSyncDryRun, "dry-run", false, "read + filter but do not inject into orca")
	orcaSyncCmd.Flags().BoolVar(&orcaSyncSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	orcaSyncCmd.Flags().StringSliceVar(&orcaSyncDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com")
	orcaSyncCmd.Flags().StringVar(&orcaSyncCDP, "cdp", livecdp.DefaultOrcaCDP, "orca CDP endpoint (orca must run with --remote-debugging-port)")
	orcaSyncCmd.Flags().StringVar(&orcaSyncBrowser, "browser", "", "source browser name (default: source.yaml browser, then Chrome)")
}

func runOrcaSync(cmd *cobra.Command, args []string) error {
	if !orcaSyncOnce && !orcaSyncWatch {
		return fmt.Errorf("pass either --once for a single pass or --watch for the long-running watcher")
	}
	if orcaSyncOnce && orcaSyncWatch {
		return fmt.Errorf("--once and --watch are mutually exclusive")
	}

	// LoadSourceLocal, not LoadSource: the local loop has no push target, so it
	// must not require sink.url or a peer/secret. A missing source.yaml is fine
	// (defaults: default Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	if _, err := loadFreshBlocklist(); err != nil {
		return err
	}

	browserName := orcaSyncBrowser
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

	skipDBSC := orcaSyncSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

	cdp := orcaSyncCDP
	if cdp == "" {
		cdp = livecdp.DefaultOrcaCDP
	}

	// lastCookies caches the most recent read so the --watch poll can re-inject
	// newly-opened panes without re-decrypting Chrome every tick; the fsnotify
	// watch refreshes it on each Chrome cookie change. lastDBSCSkipped carries
	// the most recent DBSC-skipped count out for the --once summary.
	var (
		mu              sync.Mutex
		lastCookies     []chrome.Cookie
		lastDBSCSkipped int
	)

	readFresh := func() ([]chrome.Cookie, error) {
		blocklist, err := loadFreshBlocklist()
		if err != nil {
			return nil, err
		}
		cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		cookies = sinkpush.FilterByHostPatterns(cookies, orcaSyncDomains)
		mu.Lock()
		lastCookies = cookies
		lastDBSCSkipped = st.dbsc.skipped
		mu.Unlock()
		if orcaSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie orca-sync: read %d, blocked %d, dbsc(warn=%d skip=%d), injecting %d\n",
				st.totalRead, st.totalDropped, st.dbsc.warned, st.dbsc.skipped, len(cookies))
		}
		return cookies, nil
	}

	// syncOnce reads fresh, then injects into every open orca pane. Returns the
	// number of panes injected (0 = no pane open, which is a soft outcome).
	syncOnce := func(ctx context.Context) (int, error) {
		cookies, err := readFresh()
		if err != nil {
			return 0, err
		}
		if orcaSyncDryRun {
			fmt.Fprintf(os.Stderr, "agentcookie orca-sync: dry-run; not injecting %d cookies\n", len(cookies))
			return 0, nil
		}
		if len(cookies) == 0 {
			return 0, nil
		}
		return livecdp.InjectIntoOrca(cdp, cookies)
	}

	// injectCached re-injects the last read set into open panes without
	// re-reading Chrome. Used by the --watch poll to catch newly-opened panes.
	injectCached := func() (int, error) {
		mu.Lock()
		cookies := lastCookies
		mu.Unlock()
		if orcaSyncDryRun || len(cookies) == 0 {
			return 0, nil
		}
		return livecdp.InjectIntoOrca(cdp, cookies)
	}

	if orcaSyncOnce {
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		panes, err := syncOnce(ctx)
		if err != nil {
			// Fail soft with remediation: the usual cause is orca not running
			// with the debug port, or no browser pane open.
			return fmt.Errorf("orca-sync: %w (launch orca with --remote-debugging-port and open a browser pane -- `agentcookie orca-sync --help`)", err)
		}
		mu.Lock()
		dbsc := lastDBSCSkipped
		count := len(lastCookies)
		mu.Unlock()
		fmt.Fprint(os.Stderr, orcaSyncSummary(panes, count, dbsc))
		return nil
	}

	// --watch: re-inject on every debounced Chrome Cookies change, and also on
	// a slow poll so a pane opened after start gets seeded. A failed cycle
	// (orca down / no pane) is logged and the loop keeps running.
	w, err := watcher.New(watcher.Config{
		CookiesPath: cfg.Chrome.DBPath,
		LogLabel:    "agentcookie orca-sync --watch",
		Push:        syncOnce,
		OnEvent: func(ev watcher.Event) {
			if orcaSyncVerbose {
				fmt.Fprintf(os.Stderr, "agentcookie orca-sync --watch: %s\n", ev.String())
			}
		},
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}

	// Seed once up front so an already-open pane logs in immediately, then poll
	// for newly-opened panes alongside the fsnotify watch.
	if _, err := syncOnce(cmd.Context()); err != nil && orcaSyncVerbose {
		fmt.Fprintf(os.Stderr, "agentcookie orca-sync --watch: initial sync: %v\n", err)
	}
	go func() {
		t := time.NewTicker(orcaSyncPollInterval)
		defer t.Stop()
		for {
			select {
			case <-cmd.Context().Done():
				return
			case <-t.C:
				if n, err := injectCached(); err != nil && orcaSyncVerbose {
					fmt.Fprintf(os.Stderr, "agentcookie orca-sync --watch: poll: %v\n", err)
				} else if orcaSyncVerbose && n > 0 {
					fmt.Fprintf(os.Stderr, "agentcookie orca-sync --watch: poll injected into %d pane(s)\n", n)
				}
			}
		}
	}()

	fmt.Fprintf(os.Stderr, "agentcookie orca-sync --watch: watching %s, injecting into orca at %s\n", cfg.Chrome.DBPath, cdp)
	return w.Run(cmd.Context())
}

// orcaSyncSummary renders the --once completion line(s): how many panes were
// injected with how many cookies, an actionable note when no pane is open, and
// a DBSC note when device-bound cookies were skipped.
func orcaSyncSummary(panes, cookies, dbscSkipped int) string {
	var s string
	if panes == 0 {
		s = "agentcookie orca-sync: no orca browser pane is open -- open one in orca (it injects into open panes)\n"
	} else {
		s = fmt.Sprintf("agentcookie orca-sync: injected %d cookies into %d orca pane(s)\n", cookies, panes)
	}
	if dbscSkipped > 0 {
		s += fmt.Sprintf("agentcookie orca-sync: skipped %d device-bound (DBSC) cookies -- those sessions cannot transfer and may read as logged-out in orca\n", dbscSkipped)
	}
	return s
}
