package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/cdp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromemgr"
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
	// Skip Chrome Safe Storage entirely when CDP is managed (we never write
	// SQLite, so we never need the AES key) or when --dry-run is set.
	skipKeychain := sinkDryRun || (cfg.CDP.Enabled && cfg.CDP.Managed)
	if !skipKeychain {
		password, err := chrome.SafeStoragePassword()
		if err != nil {
			return fmt.Errorf("read Chrome Safe Storage from Keychain: %w", err)
		}
		key, err = chrome.DeriveAESKey(password)
		if err != nil {
			return err
		}
	} else if sinkDryRun {
		fmt.Fprintln(os.Stderr, "agentcookie sink: --dry-run set; skipping Chrome Safe Storage and all write paths")
	} else {
		fmt.Fprintln(os.Stderr, "agentcookie sink: cdp.managed=true; skipping Chrome Safe Storage (CDP path needs no key)")
	}

	// Start the managed Chrome subprocess if configured. The supervisor inside
	// chromemgr handles restart-on-crash for the lifetime of the sink.
	var chromeMgr *chromemgr.Manager
	if cfg.CDP.Enabled && cfg.CDP.Managed && !sinkDryRun {
		mgr, err := chromemgr.New(chromemgr.Config{
			ChromeBinary: cfg.CDP.ChromeBinary,
			ProfileDir:   cfg.CDP.ProfileDir,
		})
		if err != nil {
			return fmt.Errorf("init chromemgr: %w", err)
		}
		startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := mgr.Start(startCtx); err != nil {
			return fmt.Errorf("start managed Chrome: %w", err)
		}
		defer mgr.Stop()
		chromeMgr = mgr
		fmt.Fprintf(os.Stderr, "agentcookie sink: managed Chrome up at %s (profile=%s)\n", mustDebuggerURL(mgr), cfg.CDP.ProfileDir)
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
		CDPManaged: cfg.CDP.Enabled && cfg.CDP.Managed,
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
		if envelope.ProtocolVersion != protocol.Version {
			http.Error(w, fmt.Sprintf("protocol version mismatch: got %d, sink speaks %d", envelope.ProtocolVersion, protocol.Version), http.StatusBadRequest)
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

		written, mode, err := writeCookiesToSink(r.Context(), cfg, cookies, key, chromeMgr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: write failed after %d cookies (mode=%s): %v\n", written, mode, err)
			sinkState.LastError = err.Error()
			sinkState.LastErrorAt = time.Now().UTC()
			sinkState.TotalRejects++
			_ = stateWriter.Save(sinkState)
			http.Error(w, fmt.Sprintf("write cookies: %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "agentcookie sink: wrote %d cookies via %s (dropped %d non-allowlisted)\n", written, mode, dropped)
		sinkState.LastWrite = time.Now().UTC()
		sinkState.LastWriteCount = written
		sinkState.LastWriteMode = mode
		sinkState.TotalWrites++
		sinkState.TotalDropped += dropped
		_ = stateWriter.Save(sinkState)
		_, _ = fmt.Fprintf(w, "ok: wrote %d cookies via %s; dropped %d non-allowlisted\n", written, mode, dropped)
	})

	srv := &http.Server{Addr: cfg.Listen.Addr, Handler: mux}
	if sinkDryRun {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (dry-run; no Chrome state will be modified)\n", cfg.Listen.Addr)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (db=%s cdp=%v)\n", cfg.Listen.Addr, cfg.Chrome.DBPath, cfg.CDP.Enabled)
	}
	return srv.ListenAndServe()
}

// writeCookiesToSink chooses the appropriate write path. When CDP is managed,
// the sink writes exclusively via the managed Chrome subprocess and never
// falls back to SQLite (the managed path is the contract). When CDP is
// enabled but unmanaged, the sink probes an externally-launched Chrome
// instance and falls back to SQLite if it cannot reach it. When CDP is
// disabled entirely, SQLite is the only path.
func writeCookiesToSink(ctx context.Context, cfg *config.SinkConfig, cookies []chrome.Cookie, key []byte, mgr *chromemgr.Manager) (int, string, error) {
	if cfg.CDP.Enabled && cfg.CDP.Managed {
		if mgr == nil || !mgr.IsRunning() {
			return 0, "cdp-managed", fmt.Errorf("managed Chrome is not currently running")
		}
		wsURL, err := mgr.DebuggerURL()
		if err != nil {
			return 0, "cdp-managed", fmt.Errorf("managed Chrome debugger URL: %w", err)
		}
		dialCtx, cancelDial := context.WithTimeout(ctx, 3*time.Second)
		defer cancelDial()
		conn, derr := cdp.Dial(dialCtx, wsURL)
		if derr != nil {
			return 0, "cdp-managed", fmt.Errorf("dial managed Chrome: %w", derr)
		}
		defer conn.Close()
		callCtx, cancelCall := context.WithTimeout(ctx, 10*time.Second)
		defer cancelCall()
		written, serr := cdp.SetCookies(callCtx, conn, cookies)
		if serr != nil {
			return written, "cdp-managed", fmt.Errorf("Storage.setCookies on managed Chrome: %w", serr)
		}
		return written, "cdp-managed", nil
	}
	if cfg.CDP.Enabled {
		probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
		defer cancel()
		info, err := cdp.Probe(probeCtx, cfg.CDP.Host, cfg.CDP.Port)
		if err == nil && info.WebSocketDebuggerURL != "" {
			dialCtx, cancelDial := context.WithTimeout(ctx, 3*time.Second)
			defer cancelDial()
			conn, derr := cdp.Dial(dialCtx, info.WebSocketDebuggerURL)
			if derr == nil {
				defer conn.Close()
				callCtx, cancelCall := context.WithTimeout(ctx, 10*time.Second)
				defer cancelCall()
				written, serr := cdp.SetCookies(callCtx, conn, cookies)
				if serr == nil {
					return written, "cdp", nil
				}
				fmt.Fprintf(os.Stderr, "agentcookie sink: CDP injection failed (%v), falling back to SQLite\n", serr)
			} else {
				fmt.Fprintf(os.Stderr, "agentcookie sink: CDP dial failed (%v), falling back to SQLite\n", derr)
			}
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: CDP probe failed (%v), falling back to SQLite\n", err)
		}
	}
	written, err := chrome.WriteCookies(cfg.Chrome.DBPath, cookies, key)
	return written, "sqlite", err
}

// mustDebuggerURL formats the manager's current debugger URL with the host
// portion redacted to just the port for logging. Returns the raw URL when
// parsing fails. Used for stderr lines only.
func mustDebuggerURL(mgr *chromemgr.Manager) string {
	u, err := mgr.DebuggerURL()
	if err != nil {
		return "(not yet up)"
	}
	parsed, perr := url.Parse(u)
	if perr != nil || parsed.Port() == "" {
		return u
	}
	if _, err := strconv.Atoi(parsed.Port()); err != nil {
		return u
	}
	return "ws://127.0.0.1:" + parsed.Port() + parsed.Path
}
