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
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/transport"
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
injection via CDP, which lifts that requirement, lands in U4.`,
	RunE: runSink,
}

func runSink(cmd *cobra.Command, args []string) error {
	cfg, err := config.LoadSink(common.ConfigDir)
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
	transportSecret, err := resolveTransportSecret(common.ConfigDir, cfg.Peer.Hostname, cfg.Security.SharedSecret)
	if err != nil {
		return err
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
		var cookies []chrome.Cookie
		if err := json.Unmarshal(plaintext, &cookies); err != nil {
			http.Error(w, "unmarshal cookies: "+err.Error(), http.StatusBadRequest)
			return
		}
		written, mode, err := writeCookiesToSink(r.Context(), cfg, cookies, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: write failed after %d cookies (mode=%s): %v\n", written, mode, err)
			http.Error(w, fmt.Sprintf("write cookies: %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "agentcookie sink: wrote %d cookies via %s\n", written, mode)
		_, _ = fmt.Fprintf(w, "ok: wrote %d cookies via %s\n", written, mode)
	})

	srv := &http.Server{Addr: cfg.Listen.Addr, Handler: mux}
	fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (db=%s cdp=%v)\n", cfg.Listen.Addr, cfg.Chrome.DBPath, cfg.CDP.Enabled)
	return srv.ListenAndServe()
}

// writeCookiesToSink tries CDP live injection first when configured, falls
// back to direct SQLite write. Returns the number of cookies written and the
// mode used ("cdp" or "sqlite") so the response surfaces visibility to callers.
func writeCookiesToSink(ctx context.Context, cfg *config.SinkConfig, cookies []chrome.Cookie, key []byte) (int, string, error) {
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
