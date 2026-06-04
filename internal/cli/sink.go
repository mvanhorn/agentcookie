package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/cdp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromectl"
	"github.com/mvanhorn/agentcookie/internal/chromedirsync"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

var (
	sinkDryRun bool
)

var sinkCmd = &cobra.Command{
	Use:   "sink",
	Short: "Listen for incoming cookie syncs and upsert them into local Chrome",
	Long: `On the sink machine (your Mac mini), 'agentcookie sink' runs a long-lived
HTTP listener on the configured address. Each POST to /sync carries an
AES-GCM-sealed payload that the sink decrypts with the shared secret,
re-encrypts per cookie with this machine's Chrome Safe Storage key, and
upserts into the local Chrome cookies SQLite.

Chrome must be quit on the sink while writes happen (file lock). Live
injection via CDP, which lifts that requirement, lands in U4.

--dry-run skips the Chrome Safe Storage / SQLite / CDP write paths entirely
and dumps each accepted batch of cookies to stderr as JSON. Useful for
debugging the wire format and for running the sink over SSH without the
GUI Keychain prompt that 'security find-generic-password' otherwise
requires on macOS.`,
	RunE: runSink,
}

func init() {
	sinkCmd.Flags().BoolVar(&sinkDryRun, "dry-run", false, "accept and decrypt sync payloads but do NOT touch Chrome Safe Storage or write any cookies; dump batches to stderr")
}

func runSink(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadSink(common.ConfigDir)
	if err != nil {
		return err
	}

	// v0.12 S1: refuse to start on 0.0.0.0 or any non-tailnet address.
	// Catches the case where an old sink.yaml has the v0.11 permissive
	// default baked in, OR a user hand-edited the file. Explicit
	// 127.0.0.1 stays allowed for local-dev binding (operator typed it).
	if err := validateListenAddr(cfg.Listen.Addr); err != nil {
		return fmt.Errorf("sink listen %q: %w", cfg.Listen.Addr, err)
	}

	// v0.12.0-beta.3 headless mode: when skip_chrome_sqlite is true, the
	// sink never touches Chrome Safe Storage. Sidecar + adapter push are
	// still the cookie-delivery paths. Friends running on a fully headless
	// Mac mini (no GUI session to answer Keychain prompts) opt in via
	// wizard auto-detect.
	var key []byte
	switch {
	case sinkDryRun:
		fmt.Fprintln(os.Stderr, "agentcookie sink: --dry-run set; skipping Chrome Safe Storage and all write paths")
	case cfg.SkipChromeSQLite:
		fmt.Fprintln(os.Stderr, "agentcookie sink: skip_chrome_sqlite set; sidecar + adapter push only (no Chrome Safe Storage read, no Chrome SQLite write)")
	default:
		password, err := chrome.SafeStoragePassword()
		if err != nil {
			return fmt.Errorf("read Chrome Safe Storage from Keychain: %w (%s. To run sidecar+adapter only, set skip_chrome_sqlite: true in sink.yaml)", err, chrome.SafeStorageRemediation)
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
	}
	transportSecret, err := resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)
	if err != nil {
		return err
	}

	logSinkStartupBlocklistStatus()

	// Persistent replay-defense state. Survives sink restart so a
	// captured /sync envelope cannot be replayed after a reboot. A
	// corrupt sequence.json fails the sink boot rather than silently
	// resetting the high-water marks (which would reopen the replay
	// window). Operator recovery: delete ~/.agentcookie/sequence.json.
	home, _ := os.UserHomeDir()
	seqStore := protocol.NewFileSequenceStore(protocol.DefaultSequencePath(home))
	seqTracker, err := protocol.NewTrackerFromStore(seqStore)
	if err != nil {
		return fmt.Errorf("load replay-defense state: %w", err)
	}

	// State writer for `agentcookie status` to read.
	stateWriter := state.NewWriter(state.SinkPath(home))
	sinkState := &state.SinkState{
		Role:       "sink",
		ListenAddr: cfg.Listen.Addr,
	}

	// Opt-in cmux delivery surface (sink.yaml `cmux.enabled`). Registered
	// here, not in sinkpush.init(), because it carries config (binary
	// path, host filter) the package-load init cannot see. Once
	// registered it fires after every sync via sinkpush.RunAll alongside
	// the built-in per-CLI adapters, and shows up in `doctor` and
	// `wizard verify-adapters` for free.
	if cfg.Cmux.Enabled {
		sinkpush.Register(sinkpush.NewCmux(cfg.Cmux.CmuxPath, cfg.Cmux.DomainFilter))
		fmt.Fprintln(os.Stderr, "agentcookie sink: cmux delivery surface enabled")
	}

	mux := newSinkMux(cfg, transportSecret, key, seqTracker, stateWriter, sinkState)

	srv := httpserver.Configure(&http.Server{Addr: cfg.Listen.Addr, Handler: mux}, httpserver.SinkSync)
	if sinkDryRun {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (dry-run; no Chrome state will be modified)\n", cfg.Listen.Addr)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (db=%s)\n", cfg.Listen.Addr, cfg.Chrome.DBPath)
	}
	return srv.ListenAndServe()
}

func logSinkStartupBlocklistStatus() {
	bl, err := loadFreshBlocklist()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie sink: blocklist load failed; /sync will fail closed until fixed: %v\n", err)
		return
	}
	blockMatcher := protocol.NewBlocklistMatcher(bl)
	if blockMatcher.PatternCount() == 0 {
		fmt.Fprintln(os.Stderr, "agentcookie sink: blocklist empty; sync-all mode")
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie sink: blocklist has %d opt-out patterns\n", blockMatcher.PatternCount())
	}
}

func newSinkMux(
	cfg *config.SinkConfig,
	transportSecret string,
	key []byte,
	seqTracker *protocol.SequenceTracker,
	stateWriter *state.Writer,
	sinkState *state.SinkState,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		httpserver.LimitedReader(r, httpserver.Defaults(httpserver.SinkSync).MaxBodyBytes)
		sealed, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		plaintext, err := transport.OpenWithSecret(sealed, transportSecret)
		if err != nil {
			http.Error(w, "open payload: "+err.Error(), http.StatusUnauthorized)
			return
		}
		var envelope protocol.SyncEnvelope
		if err := json.Unmarshal(plaintext, &envelope); err != nil {
			http.Error(w, "unmarshal envelope: "+err.Error(), http.StatusBadRequest)
			return
		}
		if envelope.ProtocolVersion < protocol.MinVersion || envelope.ProtocolVersion > protocol.Version {
			http.Error(w, fmt.Sprintf("protocol version mismatch: got %d, sink speaks %d-%d", envelope.ProtocolVersion, protocol.MinVersion, protocol.Version), http.StatusBadRequest)
			return
		}

		bl, err := loadFreshBlocklist()
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: blocklist load failed: %v\n", err)
			recordSinkReject(sinkState, stateWriter, err)
			http.Error(w, "load blocklist: "+err.Error(), http.StatusInternalServerError)
			return
		}
		blockMatcher := protocol.NewBlocklistMatcher(bl)

		if !seqTracker.Accept(envelope.SourceHostname, envelope.Sequence) {
			http.Error(w, fmt.Sprintf("sequence %d not greater than last seen for %q (replay defense)", envelope.Sequence, envelope.SourceHostname), http.StatusConflict)
			return
		}

		// Sink-side blocklist filter (defense in depth).
		cookies := envelope.Cookies
		var droppedHosts map[string]int
		cookies, droppedHosts = blockMatcher.Filter(cookies)

		dropped := 0
		for _, n := range droppedHosts {
			dropped += n
		}

		if sinkDryRun {
			// Dump the accepted batch to stderr as JSON for inspection. Do NOT
			// touch Chrome state.
			dump, _ := json.MarshalIndent(map[string]any{
				"source_hostname": envelope.SourceHostname,
				"sequence":        envelope.Sequence,
				"accepted":        len(cookies),
				"dropped":         dropped,
				"cookies":         cookies,
			}, "", "  ")
			fmt.Fprintf(os.Stderr, "agentcookie sink (dry-run): accepted batch:\n%s\n", string(dump))
			sinkState.LastWrite = time.Now().UTC()
			sinkState.LastWriteCount = len(cookies)
			sinkState.LastWriteMode = "dry-run"
			sinkState.TotalWrites++
			sinkState.TotalDropped += dropped
			_ = stateWriter.Save(sinkState)
			_, _ = fmt.Fprintf(w, "dry-run ok: accepted %d cookies; dropped %d blocklisted cookies\n", len(cookies), dropped)
			return
		}

		var (
			result    writeResult
			writeMode string
			writeErr  error
		)
		if cfg.SkipChromeSQLite {
			// v0.12.0-beta.3: headless-sink path. Sidecar only; no Chrome
			// SQLite/leveldb/indexeddb writes. Friend's Chrome app on
			// the sink does not see synced cookies through this path
			// (PP CLIs read them via sidecar / adapter session files).
			result, writeErr = applySidecarOnlyToSink(cookies)
			writeMode = "sidecar+adapter"
		} else {
			result, writeErr = applyEnvelopeToSink(r.Context(), cfg, &envelope, cookies, key)
			writeMode = "sqlite+leveldb"
		}
		if writeErr != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: write failed (cookies=%d ls=%d idb=%d mode=%s): %v\n", result.Cookies, result.LocalStorage, result.IndexedDB, writeMode, writeErr)
			recordSinkReject(sinkState, stateWriter, writeErr)
			http.Error(w, fmt.Sprintf("apply envelope: %v", writeErr), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "agentcookie sink: wrote %d cookies (+ %d sidecar) + %d localStorage origins + %d indexedDB origins (mode=%s, dropped %d blocklisted cookies)\n", result.Cookies, result.SidecarCookies, result.LocalStorage, result.IndexedDB, writeMode, dropped)
		sinkState.LastWrite = time.Now().UTC()
		// In skip_chrome_sqlite mode, result.Cookies is zero (we did not
		// write Chrome SQLite); report the sidecar count so sink-state
		// reflects what actually shipped to PP CLI consumers.
		if cfg.SkipChromeSQLite {
			sinkState.LastWriteCount = result.SidecarCookies
		} else {
			sinkState.LastWriteCount = result.Cookies
		}
		sinkState.LastWriteMode = writeMode
		sinkState.TotalWrites++
		sinkState.TotalDropped += dropped

		// v0.12.0-beta.3: when CDP injection is enabled, spawn a
		// one-shot headless Chrome and push the cookies via
		// Storage.setCookies. Chrome encrypts its own SQLite with its
		// own Safe Storage key; agentcookie never reads Chrome's
		// Keychain item on this path. Failures are logged but do not
		// fail the /sync response -- the sidecar write already
		// succeeded above, so PP CLIs are still served.
		if cfg.CDP.Enabled && len(cookies) > 0 {
			profileDir := cfg.CDP.ProfileDir
			if profileDir == "" {
				profileDir = "~/.agentcookie/chrome-profile"
			}
			if cdpErr := cdpInject(r.Context(), profileDir, cookies); cdpErr != nil {
				fmt.Fprintf(os.Stderr, "agentcookie sink: CDP injection failed (sidecar write succeeded, PP CLIs unaffected): %v\n", cdpErr)
			} else {
				fmt.Fprintf(os.Stderr, "agentcookie sink: CDP injection pushed %d cookies into %s\n", len(cookies), profileDir)
				sinkState.LastWriteMode = writeMode + "+cdp"
			}
		}

		// v0.11: after the cookie write commits, push the decrypted set
		// into each registered PP CLI's local session cache. This is the
		// step that lets kooky-using AND pycookiecheat-using PP CLIs run
		// headlessly on this sink with zero per-binary Keychain prompts.
		// Adapter failures are reported but do not block the sync. See
		// plan 2026-05-17-007.
		if len(cookies) > 0 {
			adapterResults := sinkpush.RunAll(cookies)
			sinkState.LastAdapterResults = toStateAdapterResults(adapterResults)
			logAdapterResults(adapterResults)
		}

		// v0.13: secrets-bus payload. When present, persist per-CLI
		// secrets.env files at the standard path under
		// ~/.agentcookie/secrets/. Sealing is enabled when the master
		// key is present AND v0.12's sealing posture is on; the sealed
		// twin appears alongside the plaintext. R12 regression guard:
		// when envelope.Secrets is empty/nil this branch is a no-op.
		if len(envelope.Secrets) > 0 {
			home, _ := os.UserHomeDir()
			sealingEnabled := keystore.MasterKeyExists()
			secResult, secErrs := secretsbus.WritePayload(home, envelope.Secrets, sealingEnabled)
			for _, e := range secErrs {
				fmt.Fprintf(os.Stderr, "agentcookie sink: secrets-bus: %v\n", e)
			}
			fmt.Fprintf(os.Stderr, "agentcookie sink: secrets-bus wrote %d cli(s), %d key(s), %d sealed, %d file(s) materialized\n",
				secResult.CLIsWritten, secResult.KeysWritten, secResult.SealedWritten, secResult.FilesMaterialized)
		}

		_ = stateWriter.Save(sinkState)
		_, _ = fmt.Fprintf(w, "ok: wrote %d cookies (%d sidecar), %d localStorage origins, %d indexedDB origins; dropped %d blocklisted cookies\n", result.Cookies, result.SidecarCookies, result.LocalStorage, result.IndexedDB, dropped)
	})
	return mux
}

func recordSinkReject(sinkState *state.SinkState, stateWriter *state.Writer, err error) {
	if sinkState == nil {
		return
	}
	sinkState.LastError = err.Error()
	sinkState.LastErrorAt = time.Now().UTC()
	sinkState.TotalRejects++
	if stateWriter != nil {
		_ = stateWriter.Save(sinkState)
	}
}

// toStateAdapterResults converts sinkpush.Result slices to the state
// package's AdapterResult shape (state cannot import sinkpush without
// a dependency loop, so the conversion happens here at the seam).
func toStateAdapterResults(rs []sinkpush.Result) []state.AdapterResult {
	now := time.Now().UTC()
	out := make([]state.AdapterResult, len(rs))
	for i, r := range rs {
		sr := state.AdapterResult{
			Name:          r.Name,
			Pushed:        r.Pushed,
			Invalid:       r.Invalid,
			Skipped:       r.Skipped,
			SkippedReason: r.SkippedReason,
			RanAt:         now,
		}
		if r.Err != nil {
			sr.Err = r.Err.Error()
		}
		out[i] = sr
	}
	return out
}

// logAdapterResults emits one stderr line per adapter outcome. Mirrors
// the existing probe-ok one-liner shape for visual consistency in
// sink.err.log: any future regression (failed adapter, missing CLI)
// is visible at a glance.
func logAdapterResults(rs []sinkpush.Result) {
	for _, r := range rs {
		switch {
		case r.Err != nil:
			fmt.Fprintf(os.Stderr, "agentcookie sink: adapter %s FAIL: %v\n", r.Name, r.Err)
		case r.Skipped:
			fmt.Fprintf(os.Stderr, "agentcookie sink: adapter %s skipped (%s)\n", r.Name, r.SkippedReason)
		default:
			if r.Invalid > 0 {
				fmt.Fprintf(os.Stderr, "agentcookie sink: adapter %s pushed %d cookies (%d invalid dropped)\n", r.Name, r.Pushed, r.Invalid)
			} else {
				fmt.Fprintf(os.Stderr, "agentcookie sink: adapter %s pushed %d cookies\n", r.Name, r.Pushed)
			}
		}
	}
}

// writeResult counts what landed on the sink during one /sync.
type writeResult struct {
	Cookies        int
	SidecarCookies int // plaintext cookies written to ~/.agentcookie/cookies-plain.db
	LocalStorage   int // top-level origin subdirs in the live leveldb after the write
	IndexedDB      int // origin subdirs in the live IndexedDB dir after the write
}

// cdpInject is the indirection seam that lets tests stub the
// real cdp.InjectCookies call. Production wires it to the live
// chromedp-backed implementation; tests overwrite it via
// SetCDPInjectorForTesting.
var cdpInject = func(ctx context.Context, profileDir string, cookies []chrome.Cookie) error {
	return cdp.InjectCookies(ctx, profileDir, cookies)
}

// SetCDPInjectorForTesting replaces cdpInject with the given function
// and returns a restore func. Test-only seam.
func SetCDPInjectorForTesting(f func(ctx context.Context, profileDir string, cookies []chrome.Cookie) error) func() {
	prev := cdpInject
	cdpInject = f
	return func() { cdpInject = prev }
}

// writeCookiesSidecar writes the plaintext cookies sidecar at
// ~/.agentcookie/cookies-plain.db, sealed under the v0.12 master key
// when present. Shared between the legacy path (applyEnvelopeToSink)
// and the v0.12.0-beta.3 skip_chrome_sqlite path (applySidecarOnlyToSink).
func writeCookiesSidecar(cookies []chrome.Cookie) (int, error) {
	var sidecarMaster []byte
	if keystore.MasterKeyExists() {
		if mk, err := keystore.ReadMasterKey(); err == nil {
			sidecarMaster = mk
		}
	}
	return chrome.WriteCookiesSidecar(chromepaths.SidecarCookiesDB(), cookies, sidecarMaster)
}

// applySidecarOnlyToSink is the v0.12.0-beta.3 headless-sink write path.
// Writes only the plaintext-cookies sidecar; never touches Chrome
// SQLite, LocalStorage, or IndexedDB. Used when cfg.SkipChromeSQLite is
// set. The friend's Chrome app on the sink will not see synced cookies
// through this path; PP CLIs read them via the sidecar / adapter
// session files. CDP injection (Phase 2 / U5) runs as a parallel path
// from the /sync handler and is not invoked here.
func applySidecarOnlyToSink(cookies []chrome.Cookie) (writeResult, error) {
	var result writeResult
	if len(cookies) == 0 {
		return result, nil
	}
	n, err := writeCookiesSidecar(cookies)
	result.SidecarCookies = n
	if err != nil {
		return result, fmt.Errorf("sidecar: %w", err)
	}
	return result, nil
}

// applyEnvelopeToSink wraps the three on-disk Chrome writes in a single
// quit-Chrome / write / relaunch-Chrome ceremony. Direct file writes are
// the only path in v0.7: cookies via SQLite (encrypted with the
// Keychain-derived AES key), localStorage via leveldb dir replacement,
// IndexedDB via dir replacement.
func applyEnvelopeToSink(
	ctx context.Context,
	cfg *config.SinkConfig,
	env *protocol.SyncEnvelope,
	cookies []chrome.Cookie,
	key []byte,
) (writeResult, error) {
	var result writeResult
	// v0.9: WithChromeDown (not WithChromeQuit) -- on the Mac mini sink
	// Chrome stays quit. Launching Chrome would trigger the meta.version
	// migration from 18 to 24 and rewrite cookies into App-Bound v20,
	// breaking every kooky v0.2.2 reader. See plan 2026-05-17-003 U5.
	err := chromectl.WithChromeDown(ctx, 20*time.Second, func() error {
		if len(cookies) > 0 {
			n, err := chrome.WriteCookies(cfg.Chrome.DBPath, cookies, key)
			result.Cookies = n
			if err != nil {
				return fmt.Errorf("cookies: %w", err)
			}
			if rowCount, qerr := chrome.SqliteRowCount(cfg.Chrome.DBPath, "cookies"); qerr == nil {
				fmt.Fprintf(os.Stderr, "agentcookie sink: post-commit verify: %d rows in cookies table (just wrote %d)\n", rowCount, n)
			}
			// v0.9 probe: decrypt a few rows the way kooky v0.2.2 would and
			// confirm no App-Bound prefix leakage + meta.version=18 pin.
			// Fails loud in sink stderr so a regression is visible before
			// any agent run hits broken cookies.
			if probe, perr := chrome.ProbeCookiesFile(cfg.Chrome.DBPath, key, 3); perr != nil {
				fmt.Fprintf(os.Stderr, "agentcookie sink: %s (error: %v)\n", "probe error", perr)
			} else {
				fmt.Fprintf(os.Stderr, "agentcookie sink: %s\n", probe.Summary())
			}
			// v0.8 bridge: also write a sidecar at
			// ~/.agentcookie/cookies-plain.db. PP CLIs reading via
			// pkg/sidecar get cookies without Keychain prompts and
			// without kooky's App-Bound-decryption complaint. Sidecar
			// errors are logged but non-fatal: the real Chrome write
			// is the source of truth.
			//
			// v0.12: if the agentcookie-master Keychain item exists
			// (set up by `wizard install`), the sink seals each value
			// before writing. PP CLIs that link pkg/sidecar.ReadSidecar
			// unseal transparently; older PP CLIs that read `value`
			// directly see opaque envelopes (a v0.12 transition cost
			// resolved by U12).
			if sidecarN, sidecarErr := writeCookiesSidecar(cookies); sidecarErr != nil {
				fmt.Fprintf(os.Stderr, "agentcookie sink: sidecar write failed (%v); PP CLIs will fall back to Chrome's encrypted store\n", sidecarErr)
			} else {
				result.SidecarCookies = sidecarN
			}
		}
		if len(env.LocalStorageTarball) > 0 {
			n, err := replaceLevelDBDir(env.LocalStorageTarball, chromepaths.LocalStorageLevelDB())
			result.LocalStorage = n
			if err != nil {
				return fmt.Errorf("local storage: %w", err)
			}
		}
		if len(env.IndexedDBTarball) > 0 {
			n, err := replaceLevelDBDir(env.IndexedDBTarball, chromepaths.IndexedDBDir())
			result.IndexedDB = n
			if err != nil {
				return fmt.Errorf("indexed db: %w", err)
			}
		}
		return nil
	})
	return result, err
}

// replaceLevelDBDir unpacks a tarball into a staging dir adjacent to
// liveDir, then atomic-renames it over liveDir. Returns the count of
// top-level subdirs in the new live dir (a useful proxy for "origins
// installed" since both leveldb LocalStorage and IndexedDB lay out one
// subdir per origin at the top level).
func replaceLevelDBDir(payload []byte, liveDir string) (int, error) {
	stagingDir := liveDir + ".agentcookie.staging"
	_ = os.RemoveAll(stagingDir)
	if err := chromedirsync.Unpack(payload, stagingDir); err != nil {
		return 0, err
	}
	originCount := 0
	if entries, err := os.ReadDir(stagingDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				originCount++
			}
		}
	}
	if err := chromedirsync.AtomicReplaceDir(stagingDir, liveDir); err != nil {
		return originCount, err
	}
	return originCount, nil
}
