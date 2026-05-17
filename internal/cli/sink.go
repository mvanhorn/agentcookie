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

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromectl"
	"github.com/mvanhorn/agentcookie/internal/chromedirsync"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
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

	var key []byte
	if !sinkDryRun {
		password, err := chrome.SafeStoragePassword()
		if err != nil {
			return fmt.Errorf("read Chrome Safe Storage from Keychain: %w (run 'agentcookie wizard install --as sink' to trigger the one-time Keychain Always-Allow prompt)", err)
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "agentcookie sink: --dry-run set; skipping Chrome Safe Storage and all write paths")
	}
	transportSecret, err := resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)
	if err != nil {
		return err
	}

	// Sink-side blocklist (defense in depth). Empty or missing blocklist =
	// sync everything the source pushed. Patterns that match host_keys are
	// dropped on the sink side regardless of what the source sent.
	bl, _ := config.LoadBlocklist(common.ConfigDir)
	blockMatcher := protocol.NewBlocklistMatcher(bl)
	if blockMatcher.PatternCount() == 0 {
		fmt.Fprintln(os.Stderr, "agentcookie sink: blocklist empty; sync-all mode")
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie sink: blocklist has %d opt-out patterns\n", blockMatcher.PatternCount())
	}

	seqTracker := protocol.NewSequenceTracker()

	// State writer for `agentcookie status` to read.
	home, _ := os.UserHomeDir()
	stateWriter := state.NewWriter(state.SinkPath(home))
	sinkState := &state.SinkState{
		Role:       "sink",
		ListenAddr: cfg.Listen.Addr,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
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
			_, _ = fmt.Fprintf(w, "dry-run ok: accepted %d cookies; dropped %d non-allowlisted\n", len(cookies), dropped)
			return
		}

		result, err := applyEnvelopeToSink(r.Context(), cfg, &envelope, cookies, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: write failed (cookies=%d ls=%d idb=%d): %v\n", result.Cookies, result.LocalStorage, result.IndexedDB, err)
			sinkState.LastError = err.Error()
			sinkState.LastErrorAt = time.Now().UTC()
			sinkState.TotalRejects++
			_ = stateWriter.Save(sinkState)
			http.Error(w, fmt.Sprintf("apply envelope: %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "agentcookie sink: wrote %d cookies + %d localStorage origins + %d indexedDB origins (dropped %d non-allowlisted cookies)\n", result.Cookies, result.LocalStorage, result.IndexedDB, dropped)
		sinkState.LastWrite = time.Now().UTC()
		sinkState.LastWriteCount = result.Cookies
		sinkState.LastWriteMode = "sqlite+leveldb"
		sinkState.TotalWrites++
		sinkState.TotalDropped += dropped
		_ = stateWriter.Save(sinkState)
		_, _ = fmt.Fprintf(w, "ok: wrote %d cookies, %d localStorage origins, %d indexedDB origins; dropped %d non-allowlisted cookies\n", result.Cookies, result.LocalStorage, result.IndexedDB, dropped)
	})

	srv := &http.Server{Addr: cfg.Listen.Addr, Handler: mux}
	if sinkDryRun {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (dry-run; no Chrome state will be modified)\n", cfg.Listen.Addr)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (db=%s)\n", cfg.Listen.Addr, cfg.Chrome.DBPath)
	}
	return srv.ListenAndServe()
}

// writeResult counts what landed on the sink during one /sync.
type writeResult struct {
	Cookies      int
	LocalStorage int // top-level origin subdirs in the live leveldb after the write
	IndexedDB    int // origin subdirs in the live IndexedDB dir after the write
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
	err := chromectl.WithChromeQuit(ctx, 20*time.Second, 30*time.Second, func() error {
		if len(cookies) > 0 {
			n, err := chrome.WriteCookies(cfg.Chrome.DBPath, cookies, key)
			result.Cookies = n
			if err != nil {
				return fmt.Errorf("cookies: %w", err)
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
