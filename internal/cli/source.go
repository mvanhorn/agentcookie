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
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromedirsync"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
	sourceOnce     bool
	sourceWatch    bool
	sourceVerbose  bool
	sourceDryRun   bool
	sourceSkipDBSC bool
)

// dbscSummary carries the DBSC-suspect tally from one push back to the caller
// so it can be recorded in SourceState for `doctor` / `status`.
type dbscSummary struct {
	warned  int
	skipped int
	sample  []string
}

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Read local Chrome cookies, apply the blocklist, and push to the configured sink",
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
	blocklist, err := config.LoadBlocklist(common.ConfigDir)
	if err != nil {
		return err
	}

	password, err := chrome.SafeStoragePassword()
	if err != nil {
		return fmt.Errorf("read Chrome Safe Storage from Keychain: %w", err)
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}
	secret, err := resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)
	if err != nil {
		return err
	}

	// State writer for `agentcookie status` to read.
	home, _ := os.UserHomeDir()
	stateWriter := state.NewWriter(state.SourcePath(home))
	srcState := &state.SourceState{Role: "source", SinkURL: cfg.Sink.URL}

	// --skip-dbsc-suspect is also honored via env var so a LaunchAgent can
	// opt in without a flag edit.
	skipDBSC := sourceSkipDBSC || os.Getenv("AGENTCOOKIE_SKIP_DBSC_SUSPECT") == "1"

	push := func(ctx context.Context) (int, error) {
		n, dbsc, err := pushOnce(ctx, cfg, blocklist, key, secret, sourceDryRun, sourceVerbose, skipDBSC)
		if err != nil {
			srcState.TotalFailures++
			srcState.LastError = err.Error()
			srcState.LastErrorAt = time.Now().UTC()
		} else {
			srcState.TotalPushes++
			srcState.LastPushCount = n
			srcState.LastPush = time.Now().UTC()
		}
		srcState.LastDBSCWarned = dbsc.warned
		srcState.LastDBSCSkipped = dbsc.skipped
		srcState.LastDBSCSample = dbsc.sample
		_ = stateWriter.Save(srcState)
		return n, err
	}

	if sourceOnce {
		// --once mode: bound the whole push by SyncClient's timeout
		// plus a small slack for envelope packing. Pre-v0.12 this was
		// hardcoded at 60s, which was tight even for v0.10-shape
		// payloads. The inner HTTP request also bounds itself; this
		// outer cancel is the belt to the request's suspenders.
		ctx, cancel := context.WithTimeout(cmd.Context(), httpserver.Defaults(httpserver.SyncClient).ClientTimeout+30*time.Second)
		defer cancel()
		_, err := push(ctx)
		return err
	}

	// --watch mode: long-running fsnotify watcher across all three sync
	// surfaces (cookies + Local Storage + IndexedDB). v0.7 single debounce
	// window: a write to any surface coalesces into one full envelope push.
	w, err := watcher.New(watcher.Config{
		CookiesPath:     cfg.Chrome.DBPath,
		LocalStorageDir: chromepaths.LocalStorageLevelDB(),
		IndexedDBDir:    chromepaths.IndexedDBDir(),
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
	fmt.Fprintf(os.Stderr, "agentcookie source --watch: watching %s, sink=%s\n", cfg.Chrome.DBPath, cfg.Sink.URL)

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

// pushOnce performs one read+filter+push cycle. Returns the number of cookies
// successfully posted (0 on dry-run or error).
//
// v0.3 reads ALL cookies from Chrome in one pass (pattern '%') then applies
// the blocklist matcher to drop opt-out hosts. Missing or empty blocklist =
// sync everything.
func pushOnce(
	ctx context.Context,
	cfg *config.SourceConfig,
	blocklist *config.Blocklist,
	key []byte,
	secret string,
	dryRun bool,
	verbose bool,
	skipDBSC bool,
) (int, dbscSummary, error) {
	var dbsc dbscSummary

	all, err := chrome.ReadCookiesForHost(cfg.Chrome.DBPath, "%", key)
	if err != nil {
		return 0, dbsc, fmt.Errorf("read cookies: %w", err)
	}
	totalRead := len(all)

	blockMatcher := protocol.NewBlocklistMatcher(blocklist)
	all, droppedHosts := blockMatcher.Filter(all)
	totalDropped := 0
	for _, n := range droppedHosts {
		totalDropped += n
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "agentcookie source: read %d cookies, blocked %d on %d hosts, passing %d\n",
			totalRead, totalDropped, len(droppedHosts), len(all))
	}

	// DBSC-suspect pass: flag (and optionally drop) cookies that look
	// device-bound and would die on the sink. Warn mode (default) is
	// non-destructive; skip mode drops them. See internal/chrome/dbsc.go.
	dbscRes := chrome.ClassifyCookies(all, time.Now().UTC(), skipDBSC)
	all = dbscRes.Shipped
	dbsc.warned = len(dbscRes.Warned)
	dbsc.skipped = len(dbscRes.Skipped)
	dbsc.sample = dbscSampleReasons(dbscRes)
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
		"cookies_passing":      len(all),
		"cookies_dbsc_warned":  dbsc.warned,
		"cookies_dbsc_skipped": dbsc.skipped,
		"secrets_clis":         secretsCLICount,
		"dry_run":              dryRun,
		"sink_url":             cfg.Sink.URL,
		"posted":               false,
	}

	if dryRun || (len(all) == 0 && secretsCLICount == 0) {
		_ = emit(result, fmt.Sprintf("agentcookie source: %d cookies after blocklist, %d secrets clis (dry-run=%v)%s\n", len(all), secretsCLICount, dryRun, dbscNote(dbsc)))
		return 0, dbsc, nil
	}

	// v0.7: pack Local Storage and IndexedDB alongside cookies. Both are
	// directories of LevelDB files in Chrome's Default profile; we tar
	// them, the envelope carries the bytes, the sink unpacks into its
	// real Chrome profile. Errors fetching either are non-fatal so the
	// source still pushes whatever it could read.
	var lsTarball []byte
	var idbTarball []byte
	var idbSkipped []string
	if lt, _, err := chromedirsync.Pack(chromepaths.LocalStorageLevelDB(), 0); err == nil {
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
		if it, sk, err := chromedirsync.Pack(chromepaths.IndexedDBDir(), 5*1024*1024); err == nil {
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
		return 0, dbsc, fmt.Errorf("marshal envelope: %w", err)
	}
	sealed, err := transport.SealWithSecret(payload, secret)
	if err != nil {
		return 0, dbsc, fmt.Errorf("seal payload: %w", err)
	}

	// Bound the POST by the SyncClient profile's timeout (5 minutes
	// in v0.12) so a heavy LocalStorage / IndexedDB payload over a
	// slow tailnet link does not get cut off at the pre-v0.12 30s
	// floor. The Client.Timeout itself still applies; context.Done
	// is just the cooperative path that gives the handler a clean
	// cancellation.
	postCtx, cancel := context.WithTimeout(ctx, httpserver.Defaults(httpserver.SyncClient).ClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(postCtx, "POST", cfg.Sink.URL, bytes.NewReader(sealed))
	if err != nil {
		return 0, dbsc, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := httpserver.Client(httpserver.SyncClient).Do(req)
	if err != nil {
		return 0, dbsc, fmt.Errorf("POST to sink %s: %w", cfg.Sink.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	result["posted"] = resp.StatusCode == http.StatusOK
	result["sink_response"] = string(body)
	result["sink_status"] = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		return 0, dbsc, fmt.Errorf("sink returned %d: %s", resp.StatusCode, string(body))
	}
	_ = emit(result, fmt.Sprintf("agentcookie source: posted %d cookies, sink replied: %s%s\n", len(all), string(body), dbscNote(dbsc)))
	return len(all), dbsc, nil
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
