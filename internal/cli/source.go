package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromedirsync"
	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
	sourceOnce             bool
	sourceWatch            bool
	sourceVerbose          bool
	sourceDryRun           bool
	sourceSkipDBSC         bool
	sourceSkipLocalStorage bool
	sourcePullListen       string
)

// resolveSinkURL is the sink URL resolver used by pushOnce. Production
// wires it to tsclient.ResolveSinkURL; tests can override it to inject
// specific resolution behaviors (e.g., ErrAmbiguousPeer).
var resolveSinkURL = tsclient.ResolveSinkURL

// loadSecretsPayload is replaceable by tests. CDP-source profiles are
// cookie-only, so they must not inherit process-home secrets bus state.
var loadSecretsPayload = secretsbus.LoadPayloadWithDiscovery

// sourceNow is the clock for per-sink backoff and push-trigger rate caps.
// Tests replace it to step through the schedule without sleeping.
var sourceNow = time.Now

// pushTriggerMinInterval is the minimum gap between pushes started by the
// secrets-bus and discovery watchers. It matches the cookie watcher's
// default rate cap.
const pushTriggerMinInterval = 30 * time.Second

// rateCappedTrigger runs push at most once per minInterval. Calls inside the
// window are dropped: the cookie watcher's baseline tick pushes the full
// payload, secrets included, at the same interval, so a dropped trigger
// is delivered by the next tick.
type rateCappedTrigger struct {
	minInterval time.Duration
	push        func(context.Context)

	mu   sync.Mutex
	last time.Time
}

func newRateCappedTrigger(minInterval time.Duration, push func(context.Context)) *rateCappedTrigger {
	return &rateCappedTrigger{minInterval: minInterval, push: push}
}

// fire runs push unless one started less than minInterval ago. It reports
// whether push ran.
func (t *rateCappedTrigger) fire(ctx context.Context) bool {
	t.mu.Lock()
	now := sourceNow()
	if !t.last.IsZero() && now.Sub(t.last) < t.minInterval {
		t.mu.Unlock()
		return false
	}
	t.last = now
	t.mu.Unlock()
	t.push(ctx)
	return true
}

// Per-sink push backoff: after N consecutive failures a sink is skipped for
// sinkBackoffBase * 2^(N-1), capped at sinkBackoffMax. A success resets it.
const (
	sinkBackoffBase = 30 * time.Second
	sinkBackoffMax  = 30 * time.Minute
)

// errSinkAsleep marks a sink whose Tailscale peer is offline. The source
// skips it without dialing and records it as asleep, not as a failure.
var errSinkAsleep = errors.New("sink peer is offline")

func sinkBackoffDelay(consecutiveFailures int) time.Duration {
	d := sinkBackoffBase
	for i := 1; i < consecutiveFailures && d < sinkBackoffMax; i++ {
		d *= 2
	}
	return min(d, sinkBackoffMax)
}

// SetResolveSinkURLForTesting replaces resolveSinkURL with the given
// function and returns a restore func. Test-only seam.
func SetResolveSinkURLForTesting(f func(ctx context.Context, rawURL string) (string, error)) func() {
	prev := resolveSinkURL
	resolveSinkURL = f
	return func() { resolveSinkURL = prev }
}

// dbscSummary carries the DBSC-suspect tally from one push back to the caller
// so it can be recorded in SourceState for `doctor` / `status`.
type dbscSummary struct {
	warned  int
	skipped int
	sample  []string
}

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Read local Chrome cookies, apply the cookie policy, and push to the configured sink",
	Long: `Two modes:

  agentcookie source --once   one read+push cycle, then exit. Useful for cron
                              and CI. The legacy v0.1 mode.

  agentcookie source --watch  long-running. fsnotify watches Chrome's Cookies
                              SQLite for write events; on change, debounces
                              500ms and runs a push. Rate-capped at one push
                              every 2 seconds even under continuous Chrome
                              activity. This is the v0.2 default mode and the
                              one a LaunchAgent should run.

While --watch is running, the source also serves GET /pull on the pairing
port (Tailscale 100.x:9998 by default, override with --pull-listen). Client-
only sinks that cannot accept inbound HTTP poll that endpoint instead of
receiving POST /sync:

  agentcookie sink --pull-from <this-host> --pull-interval 30s`,
	RunE: runSource,
}

func init() {
	sourceCmd.Flags().BoolVar(&sourceOnce, "once", false, "single read+push cycle, then exit")
	sourceCmd.Flags().BoolVar(&sourceWatch, "watch", false, "long-running fsnotify watcher; pushes on every Chrome cookie write (debounced)")
	sourceCmd.Flags().BoolVar(&sourceVerbose, "verbose", false, "log per-pattern decisions to stderr")
	sourceCmd.Flags().BoolVar(&sourceDryRun, "dry-run", false, "read + filter but do not contact the sink")
	sourceCmd.Flags().BoolVar(&sourceSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC) instead of shipping them with a warning; also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
	sourceCmd.Flags().BoolVar(&sourceSkipLocalStorage, "skip-local-storage", false, "push cookies without the browser's Local Storage (a Linux sink, or any sink with skip_chrome_sqlite, never applies it); also honored via "+envSkipLocalStorage+"=1")
	sourceCmd.Flags().StringVar(&sourcePullListen, "pull-listen", "", "host:port for GET /pull while --watch is running (default: this machine's Tailscale 100.x:9998)")
}

// envSkipLocalStorage lets a LaunchAgent turn off Local Storage packing
// without a flag edit, like AGENTCOOKIE_SKIP_DBSC_SUSPECT.
const envSkipLocalStorage = "AGENTCOOKIE_SKIP_LOCAL_STORAGE"

// skipLocalStorage reports whether pushes should leave out the browser's
// Local Storage leveldb. It is read per push, like AGENTCOOKIE_SYNC_INDEXEDDB.
func skipLocalStorage() bool {
	return sourceSkipLocalStorage || os.Getenv(envSkipLocalStorage) == "1"
}

func runSource(cmd *cobra.Command, args []string) error {
	if !sourceOnce && !sourceWatch {
		return fmt.Errorf("pass either --once for a single pass or --watch for the long-running watcher")
	}
	if sourceOnce && sourceWatch {
		return fmt.Errorf("--once and --watch are mutually exclusive")
	}

	cfg, err := config.LoadSource(common.ConfigDir)
	if err != nil {
		return err
	}
	// v0.3: sync-all by default. Blocklist is optional; missing file is fine.
	// Explicit policy: allowlist still reloads per push below.
	// Fail fast on a broken file at startup, then reload again for each push.
	if _, err := loadFreshBlocklist(); err != nil {
		return err
	}

	var key []byte
	var sourceBrowser chrome.Browser
	if !cfg.CDPSource.Enabled {
		sourceBrowser, err = chrome.LookupBrowser(cfg.Browser.Name)
		if err != nil {
			return err
		}
		password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
		if err != nil {
			// SafeStoragePasswordFor already prefixes its error with
			// "read <service> from Keychain ..."; don't double the prefix.
			return err
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
	}
	// Per-sink transport secrets are resolved inside the fan-out loop
	// (pushOnce), not here: resolving all secrets up front would let one
	// missing per-sink key abort delivery to every sink, defeating the
	// per-sink failure isolation. A missing key isolates that one sink.
	sinks := cfg.ResolvedSinks()

	// State writer for `agentcookie status` to read.
	home, _ := os.UserHomeDir()
	statePath := sourceStatePath(cfg.CDPSource.Enabled, common.ConfigDir, home)
	stateWriter := state.NewWriter(statePath)
	srcState := newSourceState(statePath, sinks)

	// --skip-dbsc-suspect is also honored via env var so a LaunchAgent can
	// opt in without a flag edit.
	skipDBSC := sourceSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

	// Teammate mode: load teammates.yaml (absent = off, no behavior
	// change) and migrate any listed legacy push peer before the first
	// push, in --once too. --watch also hot-reloads edits.
	activeTeammates = startTeammates(cmd.Context(), common.ConfigDir, home, cfg, sourceWatch)

	push := func(ctx context.Context) (int, error) {
		return pushWithFreshBlocklist(ctx, cfg, key, sourceDryRun, sourceVerbose, skipDBSC, srcState, stateWriter)
	}

	if sourceOnce {
		// --once mode: bound the whole push by SyncClient's timeout
		// plus a small slack for envelope packing. Pre-v0.12 this was
		// hardcoded at 60s, which was tight even for v0.10-shape
		// payloads. The inner HTTP request also bounds itself; this
		// outer cancel is the belt to the request's suspenders.
		//
		// Fan-out POSTs run concurrently, each under its own per-sink
		// ClientTimeout, so one per-sink timeout plus slack covers every
		// sink: a dead sink cannot starve the healthy ones.
		perSink := httpserver.Defaults(httpserver.SyncClient).ClientTimeout
		ctx, cancel := context.WithTimeout(cmd.Context(), perSink+30*time.Second)
		defer cancel()
		_, err := push(ctx)
		return err
	}

	if err := startWatchPullListener(cmd.Context(), cfg, func(up bool, addr string) {
		recordPullListenerState(srcState, stateWriter, up, addr)
	}); err != nil {
		return err
	}
	// Teammate lifecycle: roster polling, approval, pairing notices and
	// re-pairs. Idle until teammates.yaml lists someone.
	startTeammateLifecycle(cmd.Context(), activeTeammates, home)

	if cfg.CDPSource.Enabled {
		return runCDPSourceWatch(cmd.Context(), push, cfg.CDPSource.Endpoint, sourceVerbose)
	}

	// --watch mode: long-running fsnotify watcher across all three sync
	// surfaces (cookies + Local Storage + IndexedDB). v0.7 single debounce
	// window: a write to any surface coalesces into one full envelope push.
	// When Local Storage is skipped there is nothing to push on a Local
	// Storage write, so don't watch it either (it changes constantly).
	localStorageDir := ""
	if !skipLocalStorage() {
		localStorageDir = sourceBrowser.LocalStorageLevelDB(cfg.Browser.Profile)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: skipping Local Storage (--skip-local-storage / %s=1)\n", envSkipLocalStorage)
	}
	w, err := watcher.New(watcher.Config{
		CookiesPath:     cfg.Chrome.DBPath,
		LocalStorageDir: localStorageDir,
		IndexedDBDir:    sourceBrowser.IndexedDBDir(cfg.Browser.Profile),
		Push:            push,
		OnEvent: func(ev watcher.Event) {
			if sourceVerbose {
				fmt.Fprintf(os.Stderr, "agentcookie source --watch: %s\n", ev.String())
			}
		},
	})
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agentcookie source --watch: adapter=%s watching %s, sinks=%s\n", sourceBrowser.Name, cfg.Chrome.DBPath, sinkURLList(sinks))

	// v0.13: also watch ~/.agentcookie/secrets/ so a write to a per-CLI
	// secrets.env triggers the same push pipeline as a Chrome cookie
	// change. The secrets watcher tolerates a missing root (waits for the
	// friend to create it) and fires the same push callback as the
	// cookies watcher so the payload includes whichever surface changed.
	watchHome, _ := os.UserHomeDir()
	// The secrets and discovery watchers share one rate cap so a burst of
	// file writes cannot start a push per write.
	trigger := newRateCappedTrigger(pushTriggerMinInterval, func(ctx context.Context) {
		_, _ = push(ctx)
	})
	secretsWatcher := secretsbus.NewWatcher(watchHome, 0, func(ctx context.Context) {
		trigger.fire(ctx)
	})
	go func() {
		if err := secretsWatcher.Run(cmd.Context()); err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: secrets-bus watcher exited: %v\n", err)
		}
	}()

	// v0.14: also watch the v2 discovery paths (~/.agentcookie/manifests/
	// + PP library) so dropping a new agentcookie.toml or regenerating a
	// PP CLI triggers a push without restart.
	discoveryWatcher := secretsbus.NewDiscoveryWatcher(
		secretsbus.DiscoveryConfig{HomeDir: watchHome},
		0,
		func(ctx context.Context, delta secretsbus.RegistryDelta, _ *secretsbus.Registry) {
			if sourceVerbose && (len(delta.Added)+len(delta.Removed) > 0) {
				fmt.Fprintf(os.Stderr, "agentcookie source --watch: discovery: added=%v removed=%v\n", delta.Added, delta.Removed)
			}
			trigger.fire(ctx)
		},
	)
	go func() {
		if err := discoveryWatcher.Run(cmd.Context()); err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: discovery watcher exited: %v\n", err)
		}
	}()

	return w.Run(cmd.Context())
}

// newSourceState builds the in-memory SourceState for a source run. Per-sink
// records from the previous run's state file (backoff window, consecutive
// failures, asleep marker, and history) carry over, so a restart does not
// reset a failing sink's backoff and hammer it again. A missing or unreadable
// file starts fresh. Only records for currently configured sinks carry over,
// matched by peer (or URL for a peerless sink) the same way SinkFor matches
// during a run, so a sink removed from the config drops out of status and
// doctor on the next start.
func newSourceState(statePath string, sinks []config.SinkTarget) *state.SourceState {
	legacySinkURL := ""
	if len(sinks) > 0 {
		legacySinkURL = sinks[0].URL
	}
	st := &state.SourceState{Role: "source", SinkURL: legacySinkURL}
	prev, err := state.LoadSource(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie source: ignoring unreadable %s (%v); starting with fresh per-sink state\n", statePath, err)
		return st
	}
	if prev == nil {
		return st
	}
	for _, rec := range prev.Sinks {
		if sinkConfigured(sinks, rec) {
			st.Sinks = append(st.Sinks, rec)
		}
	}
	return st
}

// sinkConfigured reports whether a saved per-sink record belongs to one of
// the configured sinks, using SinkFor's identity: peer when the record has
// one, otherwise URL among peerless sinks.
func sinkConfigured(sinks []config.SinkTarget, rec state.SinkPushState) bool {
	for _, s := range sinks {
		if rec.Peer != "" {
			if s.Peer == rec.Peer {
				return true
			}
			continue
		}
		if s.Peer == "" && s.URL == rec.URL {
			return true
		}
	}
	return false
}

func sourceStatePath(cdpSource bool, configDir, home string) string {
	if cdpSource {
		return filepath.Join(configDir, "state", "source-state.json")
	}
	return state.SourcePath(home)
}

func sourceStatePathForConfig(cfg *config.SourceConfig, configDir, home string) string {
	return sourceStatePath(cfg != nil && cfg.CDPSource.Enabled, configDir, home)
}

const cdpSourcePollInterval = 10 * time.Second

// runCDPSourceWatch polls the browser's CDP jar rather than watching encrypted
// SQLite files. CDP has no cookie-change event, so polling is deliberately
// bounded; every cycle follows the same allowlist and encrypted push path.
func runCDPSourceWatch(ctx context.Context, push func(context.Context) (int, error), endpoint string, verbose bool) error {
	if _, err := push(ctx); err != nil {
		return err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: polling loopback CDP at %s every %s\n", endpoint, cdpSourcePollInterval)
	}
	ticker := time.NewTicker(cdpSourcePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := push(ctx); err != nil && verbose {
				fmt.Fprintf(os.Stderr, "agentcookie source --watch: CDP poll failed: %v\n", err)
			}
		}
	}
}

// sourcePushMu serializes complete source push cycles. Cookie, secrets, and
// discovery watchers each invoke push independently, and the cookie watcher
// launches runOne in a goroutine (the startup push bypasses the rate cap),
// so cycles can overlap. A slower empty or fail-closed cycle must not Clear
// a newer envelope already published to GET /pull. Same reason cmux-sync
// serializes whole cycles.
var sourcePushMu sync.Mutex

func pushWithFreshBlocklist(
	ctx context.Context,
	cfg *config.SourceConfig,
	key []byte,
	dryRun bool,
	verbose bool,
	skipDBSC bool,
	srcState *state.SourceState,
	stateWriter *state.Writer,
) (int, error) {
	sourcePushMu.Lock()
	defer sourcePushMu.Unlock()

	var pullGen uint64
	if !dryRun {
		pullGen = pullPayloadCache.Begin()
	}

	blocklist, err := loadFreshBlocklist()
	var dbsc dbscSummary
	if err != nil {
		// Fail closed at the sync boundary: do not keep serving a previously
		// filtered envelope from GET /pull after policy cannot be loaded.
		pullPayloadCache.ClearIfCurrent(pullGen)
		recordSourcePushResult(srcState, stateWriter, nil, dbsc, err)
		return 0, err
	}
	results, dbsc, err := pushOnce(ctx, cfg, blocklist, key, dryRun, verbose, skipDBSC, pullGen, srcState)
	recordSourcePushResult(srcState, stateWriter, results, dbsc, err)
	if err != nil {
		return 0, err
	}
	// Aggregate per-sink outcomes: partial or total sink failure is a push
	// failure for exit-status purposes (KTD3), even though healthy sinks
	// still received the payload. errors.Join preserves each sink error's
	// chain so errors.Is (e.g. tsclient.ErrAmbiguousPeer) still matches.
	posted, sinkErrs := aggregateSinkResults(results)
	if len(sinkErrs) > 0 {
		return posted, fmt.Errorf("push failed for %d of %d sink(s): %w", len(sinkErrs), len(results), errors.Join(sinkErrs...))
	}
	return posted, nil
}

// aggregateSinkResults returns the cookie count of the most-delivered sink
// (all sinks receive the same set) and the errors of any that failed, each
// prefixed with its sink label so the joined message names the sink.
func aggregateSinkResults(results []sinkResult) (posted int, sinkErrs []error) {
	for _, r := range results {
		if r.Err != nil {
			label := r.URL
			if r.Peer != "" {
				label = r.Peer + " (" + r.URL + ")"
			}
			sinkErrs = append(sinkErrs, fmt.Errorf("%s: %w", label, r.Err))
			continue
		}
		if r.Count > posted {
			posted = r.Count
		}
	}
	return posted, sinkErrs
}

// sourceStateMu guards srcState, which push cycles and the pull listener
// supervisor both update.
var sourceStateMu sync.Mutex

// recordPullListenerState records whether GET /pull is being served
// (pull_listener: up|down) in source state.
func recordPullListenerState(srcState *state.SourceState, stateWriter *state.Writer, up bool, addr string) {
	if srcState == nil {
		return
	}
	sourceStateMu.Lock()
	defer sourceStateMu.Unlock()
	srcState.PullListener = "down"
	srcState.PullListenerAddr = ""
	if up {
		srcState.PullListener = "up"
		srcState.PullListenerAddr = addr
	}
	if stateWriter != nil {
		_ = stateWriter.Save(srcState)
	}
}

func recordSourcePushResult(
	srcState *state.SourceState,
	stateWriter *state.Writer,
	results []sinkResult,
	dbsc dbscSummary,
	err error,
) {
	if srcState == nil {
		return
	}
	sourceStateMu.Lock()
	defer sourceStateMu.Unlock()
	now := sourceNow().UTC()
	// A read/envelope-phase failure (err != nil) is recorded once at the
	// top level; no per-sink attempt happened.
	if err != nil {
		srcState.TotalFailures++
		srcState.LastError = err.Error()
		srcState.LastErrorAt = now
	}
	for _, r := range results {
		ps := srcState.SinkFor(r.Peer, r.URL)
		switch {
		case !r.SkippedUntil.IsZero():
			// Skipped inside its backoff window: no attempt, nothing to record.
		case r.Asleep:
			ps.Asleep = true
			ps.LastAsleepAt = now
		case r.Err != nil:
			ps.Asleep = false
			ps.TotalFailures++
			ps.ConsecutiveFailures++
			ps.BackoffUntil = now.Add(sinkBackoffDelay(ps.ConsecutiveFailures))
			ps.LastError = r.Err.Error()
			ps.LastErrorAt = now
			srcState.TotalFailures++
			srcState.LastError = r.Err.Error()
			srcState.LastErrorAt = now
		default:
			ps.Asleep = false
			ps.ConsecutiveFailures = 0
			ps.BackoffUntil = time.Time{}
			ps.TotalPushes++
			ps.LastPushCount = r.Count
			ps.LastPush = now
			ps.LastError = ""
			srcState.TotalPushes++
			srcState.LastPushCount = r.Count
			srcState.LastPush = now
		}
	}
	// A successful cycle with no eligible cookies or secrets has no per-sink
	// transport result, but it is still a healthy source read. pushOnce uses a
	// non-nil empty slice for that case; nil is a dry-run and intentionally
	// leaves the durable health state unchanged.
	if err == nil && results != nil && len(results) == 0 {
		srcState.TotalPushes++
		srcState.LastPushCount = 0
		srcState.LastPush = now
	}
	srcState.LastDBSCWarned = dbsc.warned
	srcState.LastDBSCSkipped = dbsc.skipped
	srcState.LastDBSCSample = dbsc.sample
	if stateWriter != nil {
		_ = stateWriter.Save(srcState)
	}
}

// sinkResult is one sink's outcome from a single fan-out push. A sink that
// was not dialed has Asleep (Tailscale reports its peer offline) or
// SkippedUntil (inside its backoff window) set and a nil Err.
type sinkResult struct {
	Peer         string
	URL          string
	Count        int // cookies posted to this sink (0 on error)
	Err          error
	Asleep       bool
	SkippedUntil time.Time
}

// sinkURLList renders sink URLs for a log line.
func sinkURLList(sinks []config.SinkTarget) string {
	urls := make([]string, 0, len(sinks))
	for _, s := range sinks {
		urls = append(urls, s.URL)
	}
	return strings.Join(urls, ", ")
}

// pushOnce performs one read+filter+push cycle. Returns the number of cookies
// successfully posted (0 on dry-run or error).
//
// v0.3 reads ALL cookies from Chrome in one pass (pattern '%') then applies
// the cookie policy matcher to drop disallowed hosts. Missing or empty
// blocklist-mode config preserves legacy sync-all behavior.
func pushOnce(
	ctx context.Context,
	cfg *config.SourceConfig,
	blocklist *config.Blocklist,
	key []byte,
	dryRun bool,
	verbose bool,
	skipDBSC bool,
	pullGen uint64,
	srcState *state.SourceState,
) ([]sinkResult, dbscSummary, error) {
	var dbsc dbscSummary

	// Shared read pipeline (decrypt -> cookie policy -> DBSC). See
	// readFilteredCookies in cookie_pipeline.go; `source` and `cmux-sync`
	// both use it so they filter identically.
	var all []chrome.Cookie
	var st readStats
	var err error
	if cfg.CDPSource.Enabled {
		all, err = readCDPSource(ctx, cfg.CDPSource.Endpoint)
		if err != nil {
			pullPayloadCache.ClearIfCurrent(pullGen)
			return nil, dbsc, fmt.Errorf("read cookies from cdp source: %w", err)
		}
		all, st = filterCookies(all, blocklist, skipDBSC, time.Now().UTC())
	} else {
		all, st, err = readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
		if err != nil {
			pullPayloadCache.ClearIfCurrent(pullGen)
			return nil, dbsc, err
		}
	}
	totalRead := st.totalRead
	totalDropped := st.totalDropped
	droppedHosts := st.droppedHosts
	dbsc = st.dbsc
	if verbose {
		fmt.Fprintf(os.Stderr, "agentcookie source: read %d cookies, filtered %d on %d hosts, passing %d\n",
			totalRead, totalDropped, len(droppedHosts), len(all))
	}

	// Only print the DBSC detail block under --verbose: in --watch mode this
	// fires on every cookie change and would flood the LaunchAgent log for
	// any user with a persistent Google cookie. The durable signal lives in
	// `agentcookie doctor` (source-state.json) and the JSON result map; the
	// per-push human summary below carries a concise count.
	if verbose {
		if n := dbsc.warned + dbsc.skipped; n > 0 {
			verb := "shipping with a warning"
			if skipDBSC {
				verb = "skipping"
			}
			fmt.Fprintf(os.Stderr, "agentcookie source: %d cookie(s) look device-bound (DBSC); %s. These likely will not work on the sink. See README: DBSC.\n", n, verb)
			for _, r := range dbsc.sample {
				fmt.Fprintf(os.Stderr, "  - %s\n", r)
			}
		}
	}

	// v0.14: combined v1 bus + v2 discovery. LoadPayloadWithDiscovery
	// runs v1 LoadPayload AND v2 Discover, reads each discovered project's
	// [secrets.file] in place, applies sync policy, and merges. v1 bus
	// wins per-key over v2 read-in-place per spec section 10.3.
	home, _ := os.UserHomeDir()
	var secretsPayload *secretsbus.Payload
	var secretsErrs []error
	if !cfg.CDPSource.Enabled {
		secretsPayload, secretsErrs = loadSecretsPayload(home)
	}
	for _, e := range secretsErrs {
		fmt.Fprintf(os.Stderr, "agentcookie source: secrets-bus: %v\n", e)
	}
	secretsCLICount := 0
	if secretsPayload != nil {
		secretsCLICount = len(secretsPayload.CLIs)
	}
	if verbose && secretsCLICount > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie source: secrets-bus: shipping %d cli(s)\n", secretsCLICount)
	}

	result := map[string]any{
		"cookies_read":         totalRead,
		"cookies_blocked":      totalDropped,
		"cookies_filtered":     totalDropped,
		"cookie_policy":        blocklist.CookiePolicySummary(),
		"cookies_passing":      len(all),
		"cookies_dbsc_warned":  dbsc.warned,
		"cookies_dbsc_skipped": dbsc.skipped,
		"secrets_clis":         secretsCLICount,
		"dry_run":              dryRun,
		"sink_url":             cfg.Sink.URL,
		"posted":               false,
	}

	if dryRun {
		_ = emit(result, fmt.Sprintf("agentcookie source: %d cookies after cookie policy (%s), %d secrets clis (dry-run=%v)%s\n", len(all), blocklist.CookiePolicySummary(), secretsCLICount, dryRun, dbscNote(dbsc)))
		return nil, dbsc, nil
	}
	if len(all) == 0 && secretsCLICount == 0 {
		_ = emit(result, fmt.Sprintf("agentcookie source: %d cookies after cookie policy (%s), %d secrets clis (dry-run=%v)%s\n", len(all), blocklist.CookiePolicySummary(), secretsCLICount, dryRun, dbscNote(dbsc)))
		// Nothing to deliver under the current policy. Drop any previously
		// cached envelope so GET /pull cannot hand a newly polling sink
		// cookies this cycle excluded. ClearIfCurrent no-ops if a newer
		// cycle already published (or began) its own payload.
		pullPayloadCache.ClearIfCurrent(pullGen)
		// A non-nil empty result records that this was a successful source
		// cycle with no delivery attempt. nil remains reserved for dry-runs,
		// which must not make source health look current.
		return []sinkResult{}, dbsc, nil
	}

	// v0.7: pack Local Storage and IndexedDB alongside cookies from the
	// configured source browser/profile. The envelope carries the bytes, the
	// sink unpacks into its real Chrome profile. Errors fetching either are
	// non-fatal so the source still pushes whatever it could read.
	// Resolve the same adapter the watcher uses. cfg.Browser.Name was already
	// validated in LoadSource, so a failure here means the config changed
	// underneath us; fail loud rather than silently packing Chrome's profile
	// (which would mismatch the cookies/localStorage/IndexedDB the watcher and
	// the rest of this push are reading from the configured browser).
	var lsTarball []byte
	var idbTarball []byte
	var idbSkipped []string
	if !cfg.CDPSource.Enabled {
		// CDP source mode intentionally carries cookies only: localStorage and
		// IndexedDB remain in the existing browser and must not be scraped from
		// an on-disk profile as a fallback.
		sourceBrowser, err := chrome.LookupBrowser(cfg.Browser.Name)
		if err != nil {
			pullPayloadCache.ClearIfCurrent(pullGen)
			return nil, dbsc, err
		}
		// --skip-local-storage skips the pack: a Linux sink (or any sink
		// with skip_chrome_sqlite) never applies it, and the leveldb is
		// often tens of MB, re-sent on every push.
		if !skipLocalStorage() {
			if lt, _, err := chromedirsync.Pack(sourceBrowser.LocalStorageLevelDB(cfg.Browser.Profile), 0); err == nil {
				lsTarball = lt
			} else if !errors.Is(err, chromedirsync.ErrSourceMissing) {
				fmt.Fprintf(os.Stderr, "agentcookie source: localStorage pack failed (%v); continuing without it\n", err)
			}
		}
		// IndexedDB is opt-in for v0.7: typical user dirs are 400MB+ (Gmail caches,
		// Slack message history) and inlining that in the JSON envelope blows
		// past the source-side POST timeout. Most PP CLIs auth via localStorage
		// or cookies; IndexedDB is rarely an auth-state surface in practice.
		// Set AGENTCOOKIE_SYNC_INDEXEDDB=1 to opt in.
		if os.Getenv("AGENTCOOKIE_SYNC_INDEXEDDB") == "1" {
			if it, sk, err := chromedirsync.Pack(sourceBrowser.IndexedDBDir(cfg.Browser.Profile), 5*1024*1024); err == nil {
				idbTarball = it
				idbSkipped = sk
			} else if !errors.Is(err, chromedirsync.ErrSourceMissing) {
				fmt.Fprintf(os.Stderr, "agentcookie source: indexedDB pack failed (%v); continuing without it\n", err)
			}
		}
	}

	envelope := protocol.SyncEnvelope{
		ProtocolVersion:     protocol.Version,
		SourceHostname:      pairing.LocalHostname(),
		Sequence:            time.Now().UnixNano(),
		Cookies:             all,
		LocalStorageTarball: lsTarball,
		IndexedDBTarball:    idbTarball,
		IndexedDBSkipped:    idbSkipped,
	}
	if secretsPayload != nil && len(secretsPayload.CLIs) > 0 {
		envelope.Secrets = secretsPayload.CLIs
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		pullPayloadCache.ClearIfCurrent(pullGen)
		return nil, dbsc, fmt.Errorf("marshal envelope: %w", err)
	}
	pullPayloadCache.StoreIfCurrent(pullGen, payload)
	// Fan out: read and filtering above happened once; only sealing and
	// transport repeat per sink. Each sink is sealed with its own key and
	// POSTed concurrently under its own timeout, so a dead sink cannot hold
	// up the others. A per-sink failure (missing key, seal error,
	// URL-resolution, or POST) is isolated to that sink and recorded. A sink
	// inside its backoff window is skipped without an attempt, and a sink
	// whose peer Tailscale reports offline is skipped as asleep.
	//
	// A legacy push peer migrated to teammate mode is never dialed again (it
	// pulls instead): it is dropped silently, not recorded as a failure, and
	// source.yaml is left untouched. While teammate history cannot be
	// checked, a named push is held and reported as failed instead (fail
	// closed).
	var sinks []config.SinkTarget
	var held []error
	for _, sink := range cfg.ResolvedSinks() {
		heldErr := activeTeammates.PushHeld(sink.Peer)
		if heldErr == nil && activeTeammates.SuppressPush(sink.Peer) {
			continue
		}
		sinks = append(sinks, sink)
		held = append(held, heldErr)
	}
	now := sourceNow()
	results := make([]sinkResult, len(sinks))
	replies := make([]string, len(sinks))
	tailnetIPOffline := newTailnetIPOfflineCheck(ctx)
	var wg sync.WaitGroup
	for i, sink := range sinks {
		results[i] = sinkResult{Peer: sink.Peer, URL: sink.URL}
		if held[i] != nil {
			results[i].Err = held[i]
			continue
		}
		if srcState != nil {
			sourceStateMu.Lock()
			until := srcState.SinkFor(sink.Peer, sink.URL).BackoffUntil
			sourceStateMu.Unlock()
			if now.Before(until) {
				results[i].SkippedUntil = until
				continue
			}
		}
		if tailnetIPOffline(sink.URL) {
			results[i].Asleep = true
			continue
		}
		wg.Add(1)
		go func(r *sinkResult, reply *string, sink config.SinkTarget) {
			defer wg.Done()
			secret, secErr := resolveSinkSecret(common.ConfigDir, sink, cfg.Security.SharedSecret)
			if secErr != nil {
				r.Err = secErr
				return
			}
			sealed, sealErr := transport.SealWithSecret(payload, secret)
			if sealErr != nil {
				r.Err = fmt.Errorf("seal payload: %w", sealErr)
				return
			}
			body, postErr := postToSink(ctx, sink.URL, sealed, verbose)
			switch {
			case errors.Is(postErr, errSinkAsleep):
				r.Asleep = true
			case postErr != nil:
				r.Err = postErr
			default:
				r.Count = len(all)
				*reply = body
			}
		}(&results[i], &replies[i], sink)
	}
	wg.Wait()

	sinkReports := make([]map[string]any, 0, len(sinks))
	humanLines := make([]string, 0, len(sinks))
	delivered := 0
	for i, r := range results {
		rep := map[string]any{"url": r.URL, "peer": r.Peer, "posted": false}
		switch {
		case !r.SkippedUntil.IsZero():
			rep["backoff_until"] = r.SkippedUntil.UTC().Format(time.RFC3339)
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] skipped: backing off after failures until %s", r.URL, r.SkippedUntil.Format(time.RFC3339)))
		case r.Asleep:
			rep["asleep"] = true
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] skipped: Tailscale reports the peer offline (asleep)", r.URL))
		case r.Err != nil:
			rep["error"] = r.Err.Error()
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] push failed: %v", r.URL, r.Err))
		default:
			delivered++
			rep["posted"] = true
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] posted %d cookies, sink replied: %s%s", r.URL, r.Count, replies[i], dbscNote(dbsc)))
		}
		sinkReports = append(sinkReports, rep)
	}

	result["sinks"] = sinkReports
	result["posted"] = delivered > 0 && len(aggregateFailedLabels(results)) == 0
	_ = emit(result, strings.Join(humanLines, "\n")+"\n")
	return results, dbsc, nil
}

// aggregateFailedLabels returns the URLs of sinks whose push failed.
func aggregateFailedLabels(results []sinkResult) []string {
	var failed []string
	for _, r := range results {
		if r.Err != nil {
			failed = append(failed, r.URL)
		}
	}
	return failed
}

// newTailnetIPOfflineCheck returns a predicate reporting whether a sink URL
// whose host is a literal Tailscale IP (100.64.0.0/10 or fd7a:115c:a1e0::/48)
// belongs to a peer Tailscale status reports offline. Such a sink is treated
// like tsclient.ErrPeerOffline: skipped as asleep without a dial. Status is
// fetched at most once per push, and only when a literal Tailscale IP sink is
// checked. When status is unavailable or no peer carries the IP, the
// predicate reports false and the sink is dialed as before.
func newTailnetIPOfflineCheck(ctx context.Context) func(rawURL string) bool {
	var (
		st     *tsclient.Status
		loaded bool
	)
	return func(rawURL string) bool {
		u, err := url.Parse(rawURL)
		if err != nil || !tsclient.IsTailscaleIP(u.Hostname()) {
			return false
		}
		if !loaded {
			loaded = true
			if got, err := tsclient.CurrentStatus(ctx); err == nil {
				st = got
			}
		}
		peer := st.PeerByIP(u.Hostname())
		return peer != nil && !peer.Online
	}
}

// resolveSinkSecret resolves the transport secret for a single sink. A sink
// with a peer requires that peer's key and never falls back to the legacy
// shared secret — a missing key isolates that sink rather than silently
// downgrading the payload to the fleet-wide shared secret. A peerless sink
// (the synthesized legacy single-sink case) uses the legacy shared secret.
func resolveSinkSecret(configDir string, sink config.SinkTarget, legacy string) (string, error) {
	if sink.Peer != "" {
		pk, err := keystore.Load(configDir, sink.Peer)
		if err != nil {
			return "", fmt.Errorf("no key for peer %q (run `agentcookie pair`): %w", sink.Peer, err)
		}
		return string(pk.Key), nil
	}
	if legacy == "" {
		return "", fmt.Errorf("no transport credential: sink has no peer and no security.shared_secret")
	}
	return legacy, nil
}

// postToSink resolves the sink URL through Tailscale (if needed) and POSTs
// the sealed payload, returning the sink's reply body. Bounded by the
// SyncClient timeout so a slow or dead sink cannot stall beyond its own
// window.
func postToSink(ctx context.Context, rawURL string, sealed []byte, verbose bool) (string, error) {
	// Resolve sink URL hostname to IP via Tailscale if needed, so a
	// MagicDNS hostname (http://grok-bot:9999/sync) works instead of a
	// frozen 100.x IP. Fail-closed errors (ErrAmbiguousPeer) abort this
	// sink. A peer Tailscale reports offline is asleep: skip it without
	// dialing. Other soft failures fall back to the raw URL so HTTP reports
	// the connection error.
	sinkURL := rawURL
	if resolved, resolveErr := resolveSinkURL(ctx, sinkURL); resolveErr != nil {
		if errors.Is(resolveErr, tsclient.ErrAmbiguousPeer) {
			return "", fmt.Errorf("resolve sink URL: %w", resolveErr)
		}
		if errors.Is(resolveErr, tsclient.ErrPeerOffline) {
			return "", fmt.Errorf("%w: %w", errSinkAsleep, resolveErr)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "agentcookie source: sink URL resolution failed (%v); using original %s\n", resolveErr, sinkURL)
		}
	} else if resolved != sinkURL {
		if verbose {
			fmt.Fprintf(os.Stderr, "agentcookie source: resolved sink URL %s -> %s\n", sinkURL, resolved)
		}
		sinkURL = resolved
	}

	postCtx, cancel := context.WithTimeout(ctx, httpserver.Defaults(httpserver.SyncClient).ClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(postCtx, "POST", sinkURL, bytes.NewReader(sealed))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := httpserver.Client(httpserver.SyncClient).Do(req)
	if err != nil {
		return "", fmt.Errorf("POST to sink %s: %w", sinkURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sink returned %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// dbscNote returns a concise " (N DBSC-suspect: warned/skipped)" suffix for the
// per-push human summary, or "" when nothing was flagged. Keeps the daemon's
// single summary line informative without the verbose per-cookie block.
func dbscNote(d dbscSummary) string {
	if d.warned == 0 && d.skipped == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d DBSC-suspect: %d warned, %d skipped)", d.warned+d.skipped, d.warned, d.skipped)
}

// dbscSampleReasons returns up to three reason strings (warns first, then
// skips) for surfacing in logs and SourceState without flooding output.
func dbscSampleReasons(res chrome.DBSCResult) []string {
	const max = 3
	out := make([]string, 0, max)
	for _, r := range res.Warned {
		if len(out) == max {
			return out
		}
		out = append(out, r)
	}
	for _, r := range res.Skipped {
		if len(out) == max {
			return out
		}
		out = append(out, r)
	}
	return out
}

// emit writes machine output or human output depending on --json. The human
// string is the fallback text to print when --json is not set.
func emit(machine map[string]any, human string) error {
	if common.JSON {
		return json.NewEncoder(os.Stdout).Encode(machine)
	}
	_, err := fmt.Fprint(os.Stderr, human)
	return err
}
