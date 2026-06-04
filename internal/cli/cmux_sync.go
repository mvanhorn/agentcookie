package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
	cmuxSyncOnce     bool
	cmuxSyncWatch    bool
	cmuxSyncVerbose  bool
	cmuxSyncDryRun   bool
	cmuxSyncSkipDBSC bool
	cmuxSyncDomains  []string
	cmuxSyncCmuxPath string
	cmuxSyncBrowser  string
)

var cmuxSyncCmd = &cobra.Command{
	Use:   "cmux-sync",
	Short: "Local loop: read this machine's Chrome and inject the session into this machine's cmux browser",
	Long: `cmux-sync is the same-machine local loop. It reads this Mac's Chrome
cookies (decrypt + blocklist + DBSC filter, the same pipeline source uses)
and injects them into this Mac's cmux WebKit browser via
cmux rpc browser.cookies.set, so an agent driving cmux's browser pane wakes
up authenticated. No sink, no peer, no Tailscale hop.

Two modes:

  agentcookie cmux-sync --once    one read+inject cycle, then exit.
  agentcookie cmux-sync --watch   long-running; fsnotify watches Chrome's
                                  Cookies SQLite and re-injects on change.

Run it from inside cmux (a cmux child) and it passes cmux's default
socketControlMode "cmuxOnly" with no cmux change. To run it unattended via
launchd, set automation.socketControlMode to allowAll or password in
~/.config/cmux/cmux.json and restart cmux (see` + " `agentcookie doctor`" + `).`,
	RunE: runCmuxSync,
}

func init() {
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncOnce, "once", false, "single read+inject cycle, then exit")
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncWatch, "watch", false, "long-running fsnotify watcher; re-injects on every Chrome cookie write (debounced)")
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncVerbose, "verbose", false, "log per-cycle counts to stderr")
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncDryRun, "dry-run", false, "read + filter but do not inject into cmux")
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC); also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	cmuxSyncCmd.Flags().StringSliceVar(&cmuxSyncDomains, "domain", nil, "limit to these host_key LIKE patterns (repeatable), e.g. --domain %github.com; overrides cmux.domain_filter")
	cmuxSyncCmd.Flags().StringVar(&cmuxSyncCmuxPath, "cmux-path", "", "override the cmux CLI path (default: cmux.cmux_path, then the app bundle)")
	cmuxSyncCmd.Flags().StringVar(&cmuxSyncBrowser, "browser", "", "source browser name (default: source.yaml browser, then Chrome)")
}

func runCmuxSync(cmd *cobra.Command, args []string) error {
	if !cmuxSyncOnce && !cmuxSyncWatch {
		return fmt.Errorf("pass either --once for a single pass or --watch for the long-running watcher")
	}
	if cmuxSyncOnce && cmuxSyncWatch {
		return fmt.Errorf("--once and --watch are mutually exclusive")
	}

	// LoadSourceLocal, not LoadSource: the local loop has no push target,
	// so it must not require sink.url or a peer/secret. A missing
	// source.yaml is fine (defaults: default Chrome path, no blocklist).
	cfg, err := config.LoadSourceLocal(common.ConfigDir)
	if err != nil {
		return err
	}
	// Blocklist is optional; fail fast on a broken file, reload per cycle.
	if _, err := loadFreshBlocklist(); err != nil {
		return err
	}

	browserName := cmuxSyncBrowser
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

	skipDBSC := cmuxSyncSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

	// cmux target: flags override config.
	cmuxPath := cmuxSyncCmuxPath
	if cmuxPath == "" {
		cmuxPath = cfg.Cmux.CmuxPath
	}
	domainFilter := cmuxSyncDomains
	if len(domainFilter) == 0 {
		domainFilter = cfg.Cmux.DomainFilter
	}
	adapter := sinkpush.NewCmux(cmuxPath, domainFilter)
	if !adapter.IsInstalled() {
		return fmt.Errorf("cmux CLI not found at %s (install cmux, or set --cmux-path / cmux.cmux_path in source.yaml)", adapter.CLIBinary())
	}

	syncOnce := func(ctx context.Context) (int, error) {
		blocklist, err := loadFreshBlocklist()
		if err != nil {
			return 0, err
		}
		cookies, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
		if err != nil {
			return 0, err
		}
		// Apply the cmux domain filter the same way RunAll would before Push.
		cookies = sinkpush.FilterByHostPatterns(cookies, domainFilter)
		if cmuxSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie cmux-sync: read %d, blocked %d, dbsc(warn=%d skip=%d), injecting %d\n",
				st.totalRead, st.totalDropped, st.dbsc.warned, st.dbsc.skipped, len(cookies))
		}
		if cmuxSyncDryRun {
			fmt.Fprintf(os.Stderr, "agentcookie cmux-sync: dry-run; not injecting %d cookies\n", len(cookies))
			return 0, nil
		}
		if len(cookies) == 0 {
			return 0, nil
		}
		if err := adapter.Push(cookies); err != nil {
			return 0, err
		}
		return len(cookies), nil
	}

	if cmuxSyncOnce {
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		n, err := syncOnce(ctx)
		if err != nil {
			// Fail soft on cmux-side problems (down / cmuxOnly-gated): name the
			// remediation, exit non-zero so a one-shot caller sees the failure.
			return fmt.Errorf("cmux-sync: %w (if cmux is running, check socketControlMode -- `agentcookie doctor` prints the fix)", err)
		}
		fmt.Fprintf(os.Stderr, "agentcookie cmux-sync: injected %d cookies into cmux\n", n)
		return nil
	}

	// --watch: re-inject on every debounced Chrome Cookies change. A failed
	// cycle (cmux down) is logged and the watcher keeps running; the next
	// change retries.
	w, err := watcher.New(watcher.Config{
		CookiesPath: cfg.Chrome.DBPath,
		LogLabel:    "agentcookie cmux-sync --watch",
		Push:        syncOnce,
		OnEvent: func(ev watcher.Event) {
			if cmuxSyncVerbose {
				fmt.Fprintf(os.Stderr, "agentcookie cmux-sync --watch: %s\n", ev.String())
			}
		},
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agentcookie cmux-sync --watch: watching %s, injecting into cmux\n", cfg.Chrome.DBPath)
	return w.Run(cmd.Context())
}
