package cli

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sync"
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
	cmuxSyncVerify   bool
	cmuxSyncDomains  []string
	cmuxSyncCmuxPath string
	cmuxSyncBrowser  string
)

// cmuxSyncPasswordFor is the Keychain reader seam; tests stub it to avoid real
// Keychain calls when exercising flag-validation and exit-0 paths.
var cmuxSyncPasswordFor = chrome.SafeStoragePasswordFor

// cmuxExitFunc is os.Exit; tests override it to intercept clean exits without
// terminating the test process.
var cmuxExitFunc = os.Exit

var cmuxSyncCmd = &cobra.Command{
	Use:   "cmux-sync",
	Short: "Local loop: read this machine's Chrome and inject the session into this machine's cmux browser",
	Long: `cmux-sync is the same-machine local loop. It reads this Mac's Chrome
cookies (decrypt + cookie policy + DBSC filter, the same pipeline source uses)
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
	cmuxSyncCmd.Flags().BoolVar(&cmuxSyncVerify, "verify", true, "after a --once push, verify each browser-bound-session host (e.g. github.com) actually authenticates and report honestly (delivery != authentication); ignored in --watch")
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
	// Cookie policy is optional; fail fast on a broken file, reload per cycle.
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
	password, err := cmuxSyncPasswordFor(sourceBrowser)
	if err != nil {
		if cmuxSyncWatch && chrome.IsKeychainAccessError(err) {
			// In watch mode, a Keychain access failure means the binary has no
			// grant yet. Exit 0 so launchd's KeepAlive does not restart the
			// agent into a prompt storm. The operator must run wizard
			// set-keychain-access before re-enabling the loop.
			fmt.Fprintf(os.Stderr, "agentcookie cmux-sync --watch: Keychain not accessible; exiting cleanly so launchd does not restart.\nFix: %s\n", chrome.SafeStorageRemediation)
			cmuxExitFunc(0)
			return nil // unreachable in production; allows test assertions
		}
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

	// lastPushed tracks the per-cookie content hash of the last successful
	// injection (watch mode only; nil in --once). Injected cookies persist
	// at cmux's WKWebsiteDataStore profile level, so a cookie only needs
	// re-pushing when its content actually changes -- without this, every
	// debounced Chrome fs-event re-injected the full multi-thousand-cookie
	// set (~every 30s, forever).
	var lastPushed map[string]uint64
	if cmuxSyncWatch {
		lastPushed = map[string]uint64{}
	}

	// The watcher fires pushes as goroutines (the startup push in
	// particular bypasses the rate cap), so two cycles can overlap. They
	// would both read the same lastPushed snapshot, compute the same
	// "everything is new" delta, and double-push the full set -- and race
	// on the map. Serialize whole cycles.
	var cycleMu sync.Mutex

	syncOnce := func(ctx context.Context) (int, error) {
		cycleMu.Lock()
		defer cycleMu.Unlock()
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
		push := cookies
		if lastPushed != nil {
			push = deltaCookies(cookies, lastPushed)
		}
		if cmuxSyncVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie cmux-sync: read %d, filtered %d, dbsc(warn=%d skip=%d), injecting %d (of %d)\n",
				st.totalRead, st.totalDropped, st.dbsc.warned, st.dbsc.skipped, len(push), len(cookies))
		}
		if cmuxSyncDryRun {
			fmt.Fprintf(os.Stderr, "agentcookie cmux-sync: dry-run; not injecting %d cookies\n", len(push))
			return 0, nil
		}
		if len(push) == 0 {
			return 0, nil
		}
		if err := adapter.Push(push); err != nil {
			return 0, err
		}
		if lastPushed != nil {
			// Rebuild from the full current set, not just the delta: every
			// cookie in it is either unchanged-since-pushed or just pushed,
			// and rebuilding prunes entries for cookies Chrome deleted.
			lastPushed = hashCookieSet(cookies)
		}
		return len(push), nil
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
		// Delivery is not authentication. For browser-bound-session hosts the
		// cookies land but the session may still be rejected server-side; verify
		// empirically and report honestly. Non-fatal: a probe never changes the
		// exit status of a successful push.
		if cmuxSyncVerify && !cmuxSyncDryRun {
			reportCmuxVerify(adapter, domainFilter, os.Stderr)
		}
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

// cmuxVerifier is the subset of CmuxAdapter reportCmuxVerify needs; an
// interface so the reporting glue is testable without a live cmux.
type cmuxVerifier interface {
	Verify(specs []sinkpush.VerifySpec) []sinkpush.VerifyResult
}

// reportCmuxVerify runs the post-injection auth probe for the browser-bound
// -session hosts in scope and prints one honest line per host to w. It never
// errors: a session that cannot be probed reports "unknown" and a
// delivered-but-not-authenticated session prints the native-login / gh-CLI
// guidance.
func reportCmuxVerify(v cmuxVerifier, domainFilter []string, w io.Writer) {
	specs := sinkpush.VerifySpecsForHosts(domainFilter)
	if len(specs) == 0 {
		return
	}
	for _, r := range v.Verify(specs) {
		switch r.State {
		case sinkpush.AuthYes:
			fmt.Fprintf(w, "agentcookie cmux-sync: session check: %s authenticated\n", r.Host)
		case sinkpush.AuthNo:
			fmt.Fprintf(w, "agentcookie cmux-sync: session check: %s NOT authenticated -- %s\n", r.Host, r.Detail)
		default:
			fmt.Fprintf(w, "agentcookie cmux-sync: session check: %s unknown (%s)\n", r.Host, r.Detail)
		}
	}
}

// cmuxCookieKey identifies a cookie across sync cycles. Host+name+path is
// the same identity WebKit upserts on, so a changed value under the same
// key is an update, not a new cookie.
func cmuxCookieKey(c chrome.Cookie) string {
	return c.HostKey + "\x00" + c.Name + "\x00" + c.Path
}

// cmuxCookieHash digests the fields that matter to injection. Any change
// flips the hash and re-queues the cookie for push.
func cmuxCookieHash(c chrome.Cookie) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\x00%d", c.Value, c.ExpiresUTC, c.IsSecure, c.IsHTTPOnly, c.SameSite)
	return h.Sum64()
}

// deltaCookies returns the cookies that are new or changed relative to
// the last successfully pushed set.
func deltaCookies(cookies []chrome.Cookie, lastPushed map[string]uint64) []chrome.Cookie {
	var out []chrome.Cookie
	for _, c := range cookies {
		if h, ok := lastPushed[cmuxCookieKey(c)]; !ok || h != cmuxCookieHash(c) {
			out = append(out, c)
		}
	}
	return out
}

// hashCookieSet builds the lastPushed map for a full cookie set.
func hashCookieSet(cookies []chrome.Cookie) map[string]uint64 {
	m := make(map[string]uint64, len(cookies))
	for _, c := range cookies {
		m[cmuxCookieKey(c)] = cmuxCookieHash(c)
	}
	return m
}
