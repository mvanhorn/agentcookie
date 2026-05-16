package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"

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
		plaintext, err := transport.OpenWithSecret(sealed, cfg.Security.SharedSecret)
		if err != nil {
			http.Error(w, "open payload: "+err.Error(), http.StatusUnauthorized)
			return
		}
		var cookies []chrome.Cookie
		if err := json.Unmarshal(plaintext, &cookies); err != nil {
			http.Error(w, "unmarshal cookies: "+err.Error(), http.StatusBadRequest)
			return
		}
		written, err := chrome.WriteCookies(cfg.Chrome.DBPath, cookies, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: write failed after %d cookies: %v\n", written, err)
			http.Error(w, fmt.Sprintf("write cookies: %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "agentcookie sink: wrote %d cookies to %s\n", written, cfg.Chrome.DBPath)
		_, _ = fmt.Fprintf(w, "ok: wrote %d cookies\n", written)
	})

	srv := &http.Server{Addr: cfg.Listen.Addr, Handler: mux}
	fmt.Fprintf(os.Stderr, "agentcookie sink: listening on http://%s (db=%s)\n", cfg.Listen.Addr, cfg.Chrome.DBPath)
	return srv.ListenAndServe()
}
