// Command spike-source reads cookies for one host from the local Chrome
// SQLite/Keychain stack and POSTs them to a spike-sink running on another
// machine. Spike-only: hardcoded shared secret, no allowlist enforcement beyond
// the host filter on the command line, single shot.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

// SpikeSecret is the AES-GCM shared secret used by both source and sink during
// the spike. It is intentionally hardcoded and ships in plaintext source; v0.1
// replaces it with a pairing-derived per-peer key.
const SpikeSecret = "agentcookie-spike-shared-secret-not-for-production-use"

func main() {
	var (
		sinkURL = flag.String("sink", "http://127.0.0.1:9999/sync", "spike-sink endpoint")
		host    = flag.String("host", "example.com", "host_key LIKE pattern (use % for wildcards, e.g. %instacart.com)")
		dbPath  = flag.String("db", chrome.DefaultCookiesPath(), "source Chrome cookies SQLite path")
		dryRun  = flag.Bool("dry-run", false, "print cookies to stderr and exit without contacting sink")
	)
	flag.Parse()

	pattern := *host
	if !containsWildcard(pattern) {
		pattern = "%" + pattern + "%"
	}

	password, err := chrome.SafeStoragePassword()
	if err != nil {
		log.Fatalf("keychain: %v", err)
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		log.Fatalf("derive key: %v", err)
	}

	cookies, err := chrome.ReadCookiesForHost(*dbPath, pattern, key)
	if err != nil {
		log.Fatalf("read cookies: %v", err)
	}
	fmt.Fprintf(os.Stderr, "spike-source: read %d cookies matching %q from %s\n", len(cookies), pattern, *dbPath)
	if len(cookies) == 0 {
		fmt.Fprintln(os.Stderr, "spike-source: nothing to send")
		os.Exit(0)
	}
	for _, c := range cookies {
		fmt.Fprintf(os.Stderr, "  %s/%s (value=%d bytes)\n", c.HostKey, c.Name, len(c.Value))
	}

	if *dryRun {
		fmt.Fprintln(os.Stderr, "spike-source: --dry-run set, not contacting sink")
		return
	}

	payload, err := json.Marshal(cookies)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	sealed, err := transport.SealWithSecret(payload, SpikeSecret)
	if err != nil {
		log.Fatalf("seal: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", *sinkURL, bytes.NewReader(sealed))
	if err != nil {
		log.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("post to sink: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Fprintf(os.Stderr, "spike-source: sink responded %d: %s\n", resp.StatusCode, string(body))
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

func containsWildcard(s string) bool {
	for _, r := range s {
		if r == '%' || r == '_' {
			return true
		}
	}
	return false
}
