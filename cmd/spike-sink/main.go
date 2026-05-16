// Command spike-sink runs an HTTP listener that accepts an AES-GCM-encrypted
// payload of cookies, decrypts it with the shared spike secret, and upserts
// each cookie into this machine's Chrome cookies SQLite. Chrome must NOT be
// running while spike-sink writes (file lock); CDP live injection arrives in
// U4 of the plan.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

// SpikeSecret matches spike-source; replaced by per-peer keys in v0.1.
const SpikeSecret = "agentcookie-spike-shared-secret-not-for-production-use"

func main() {
	var (
		addr   = flag.String("addr", "127.0.0.1:9999", "listen address")
		dbPath = flag.String("db", chrome.DefaultCookiesPath(), "destination Chrome cookies SQLite path")
	)
	flag.Parse()

	password, err := chrome.SafeStoragePassword()
	if err != nil {
		log.Fatalf("keychain: %v", err)
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		log.Fatalf("derive key: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
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
		plaintext, err := transport.OpenWithSecret(sealed, SpikeSecret)
		if err != nil {
			http.Error(w, "open payload: "+err.Error(), http.StatusUnauthorized)
			return
		}
		var cookies []chrome.Cookie
		if err := json.Unmarshal(plaintext, &cookies); err != nil {
			http.Error(w, "unmarshal cookies: "+err.Error(), http.StatusBadRequest)
			return
		}
		written, err := chrome.WriteCookies(*dbPath, cookies, key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spike-sink: write failed after %d cookies: %v\n", written, err)
			http.Error(w, fmt.Sprintf("write cookies: %v", err), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(os.Stderr, "spike-sink: wrote %d cookies to %s\n", written, *dbPath)
		fmt.Fprintf(w, "ok: wrote %d cookies\n", written)
	})

	srv := &http.Server{Addr: *addr, Handler: mux}
	fmt.Fprintf(os.Stderr, "spike-sink: listening on http://%s\n", *addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
