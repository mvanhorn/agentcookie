package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	sourceOnce     bool
	sourceWatch    bool
	sourceVerbose  bool
	sourceDryRun   bool
	sourceSkipDBSC bool
)

// resolveSinkURL is the sink URL resolver used by pushOnce. Production
// wires it to tsclient.ResolveSinkURL; tests can override it to inject
// specific resolution behaviors (e.g., ErrAmbiguousPeer).
var resolveSinkURL = tsclient.ResolveSinkURL

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
                              one a LaunchAgent should run.`,
	RunE: runSource,
}

func init() {
	sourceCmd.Flags().BoolVar(&sourceOnce, "once", false, "single read+push cycle, then exit")
	sourceCmd.Flags().BoolVar(&sourceWatch, "watch", false, "long-running fsnotify watcher; pushes on every Chrome cookie write (debounced)")
	sourceCmd.Flags().BoolVar(&sourceVerbose, "verbose", false, "log per-pattern decisions to stderr")
	sourceCmd.Flags().BoolVar(&sourceDryRun, "dry-run", false, "read + filter but do not contact the sink")
	sourceCmd.Flags().BoolVar(&sourceSkipDBSC, "skip-dbsc-suspect", false, "drop cookies that look device-bound (DBSC) instead of shipping them with a warning; also honored via AGENTCOOKIE_SKIP_DBSC_SUSPECT=1")
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

	sourceBrowser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return err
	}
	password, err := chrome.SafeStoragePasswordFor(sourceBrowser)
	if err != nil {
		// SafeStoragePasswordFor already prefixes its error with
		// "read <service> from Keychain ..."; don't double the prefix.
		return err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}
	// Per-sink transport secrets are resolved inside the fan-out loop
	// (pushOnce), not here: resolving all secrets up front would let one
	// missing per-sink key abort delivery to every sink, defeating the
	// per-sink failure isolation. A missing key isolates that one sink.
	sinks := cfg.ResolvedSinks()

	// State writer for `agentcookie status` to read.
	home, _ := os.UserHomeDir()
	stateWriter := state.NewWriter(state.SourcePath(home))
	legacySinkURL := ""
	if len(sinks) > 0 {
		legacySinkURL = sinks[0].URL
	}
	srcState := &state.SourceState{Role: "source", SinkURL: legacySinkURL}

	// --skip-dbsc-suspect is also honored via env var so a LaunchAgent can
	// opt in without a flag edit.
	skipDBSC := sourceSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

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
		// Scale the outer budget by sink count: fan-out POSTs run
		// sequentially, so a first sink that runs to its full per-sink
		// timeout must not exhaust the shared deadline and starve later
		// healthy sinks (which would defeat per-sink isolation). One
		// per-sink ClientTimeout per sink, plus slack.
		nSinks := len(sinks)
		if nSinks < 1 {
			nSinks = 1
		}
		perSink := httpserver.Defaults(httpserver.SyncClient).ClientTimeout
		ctx, cancel := context.WithTimeout(cmd.Context(), time.Duration(nSinks)*perSink+30*time.Second)
		defer cancel()
		_, err := push(ctx)
		return err
	}

	// --watch mode: long-running fsnotify watcher across all three sync
	// surfaces (cookies + Local Storage + IndexedDB). v0.7 single debounce
	// window: a write to any surface coalesces into one full envelope push.
	w, err := watcher.New(watcher.Config{
		CookiesPath:     cfg.Chrome.DBPath,
		LocalStorageDir: sourceBrowser.LocalStorageLevelDB(cfg.Browser.Profile),
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
	secretsWatcher := secretsbus.NewWatcher(watchHome, 0, func(ctx context.Context) {
		_, _ = push(ctx)
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
			_, _ = push(ctx)
		},
	)
	go func() {
		if err := discoveryWatcher.Run(cmd.Context()); err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: discovery watcher exited: %v\n", err)
		}
	}()

	return w.Run(cmd.Context())
}

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
	blocklist, err := loadFreshBlocklist()
	var dbsc dbscSummary
	if err != nil {
		recordSourcePushResult(srcState, stateWriter, nil, dbsc, err)
		return 0, err
	}
	results, dbsc, err := pushOnce(ctx, cfg, blocklist, key, dryRun, verbose, skipDBSC)
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
	now := time.Now().UTC()
	// A read/envelope-phase failure (err != nil) is recorded once at the
	// top level; no per-sink attempt happened.
	if err != nil {
		srcState.TotalFailures++
		srcState.LastError = err.Error()
		srcState.LastErrorAt = now
	}
	for _, r := range results {
		ps := srcState.SinkFor(r.Peer, r.URL)
		if r.Err != nil {
			ps.TotalFailures++
			ps.LastError = r.Err.Error()
			ps.LastErrorAt = now
			srcState.TotalFailures++
			srcState.LastError = r.Err.Error()
			srcState.LastErrorAt = now
		} else {
			ps.TotalPushes++
			ps.LastPushCount = r.Count
			ps.LastPush = now
			ps.LastError = ""
			srcState.TotalPushes++
			srcState.LastPushCount = r.Count
			srcState.LastPush = now
		}
	}
	srcState.LastDBSCWarned = dbsc.warned
	srcState.LastDBSCSkipped = dbsc.skipped
	srcState.LastDBSCSample = dbsc.sample
	if stateWriter != nil {
		_ = stateWriter.Save(srcState)
	}
}

// sinkResult is one sink's outcome from a single fan-out push.
type sinkResult struct {
	Peer  string
	URL   string
	Count int // cookies posted to this sink (0 on error)
	Err   error
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
) ([]sinkResult, dbscSummary, error) {
	var dbsc dbscSummary

	// Shared read pipeline (decrypt -> cookie policy -> DBSC). See
	// readFilteredCookies in cookie_pipeline.go; `source` and `cmux-sync`
	// both use it so they filter identically.
	all, st, err := readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, time.Now().UTC())
	if err != nil {
		return nil, dbsc, err
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
	secretsPayload, secretsErrs := secretsbus.LoadPayloadWithDiscovery(home)
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

	if dryRun || (len(all) == 0 && secretsCLICount == 0) {
		_ = emit(result, fmt.Sprintf("agentcookie source: %d cookies after cookie policy (%s), %d secrets clis (dry-run=%v)%s\n", len(all), blocklist.CookiePolicySummary(), secretsCLICount, dryRun, dbscNote(dbsc)))
		return nil, dbsc, nil
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
	sourceBrowser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return nil, dbsc, err
	}
	var lsTarball []byte
	var idbTarball []byte
	var idbSkipped []string
	if lt, _, err := chromedirsync.Pack(sourceBrowser.LocalStorageLevelDB(cfg.Browser.Profile), 0); err == nil {
		lsTarball = lt
	} else if !errors.Is(err, chromedirsync.ErrSourceMissing) {
		fmt.Fprintf(os.Stderr, "agentcookie source: localStorage pack failed (%v); continuing without it\n", err)
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
		return nil, dbsc, fmt.Errorf("marshal envelope: %w", err)
	}
	// Fan out: read and filtering above happened once; only sealing and
	// transport repeat per sink. Each sink is sealed with its own key and
	// POSTed independently. A per-sink failure (missing key, seal error,
	// URL-resolution, or POST) is isolated to that sink and recorded; the
	// loop continues so the other sinks still receive the payload.
	sinks := cfg.ResolvedSinks()
	results := make([]sinkResult, 0, len(sinks))
	sinkReports := make([]map[string]any, 0, len(sinks))
	var humanLines []string
	for _, sink := range sinks {
		r := sinkResult{Peer: sink.Peer, URL: sink.URL}
		secret, secErr := resolveSinkSecret(common.ConfigDir, sink, cfg.Security.SharedSecret)
		if secErr != nil {
			r.Err = secErr
		} else if sealed, sealErr := transport.SealWithSecret(payload, secret); sealErr != nil {
			r.Err = fmt.Errorf("seal payload: %w", sealErr)
		} else if reply, postErr := postToSink(ctx, sink.URL, sealed, verbose); postErr != nil {
			r.Err = postErr
		} else {
			r.Count = len(all)
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] posted %d cookies, sink replied: %s%s", sink.URL, len(all), reply, dbscNote(dbsc)))
		}
		if r.Err != nil {
			humanLines = append(humanLines, fmt.Sprintf("agentcookie source: [%s] push failed: %v", sink.URL, r.Err))
		}
		results = append(results, r)
		rep := map[string]any{"url": sink.URL, "peer": sink.Peer, "posted": r.Err == nil}
		if r.Err != nil {
			rep["error"] = r.Err.Error()
		}
		sinkReports = append(sinkReports, rep)
	}

	result["sinks"] = sinkReports
	result["posted"] = len(results) > 0 && len(aggregateFailedLabels(results)) == 0
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
	// sink; soft failures fall back to the raw URL so HTTP reports the
	// connection error.
	sinkURL := rawURL
	if resolved, resolveErr := resolveSinkURL(ctx, sinkURL); resolveErr != nil {
		if errors.Is(resolveErr, tsclient.ErrAmbiguousPeer) {
			return "", fmt.Errorf("resolve sink URL: %w", resolveErr)
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
