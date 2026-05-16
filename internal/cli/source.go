package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/pairing"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/watcher"
)

var (
	sourceOnce    bool
	sourceWatch   bool
	sourceVerbose bool
	sourceDryRun  bool
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Read allowlisted cookies from local Chrome and push to the configured sink",
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

	push := func(ctx context.Context) (int, error) {
		n, err := pushOnce(ctx, cfg, blocklist, key, secret, sourceDryRun, sourceVerbose)
		if err != nil {
			srcState.TotalFailures++
			srcState.LastError = err.Error()
			srcState.LastErrorAt = time.Now().UTC()
		} else {
			srcState.TotalPushes++
			srcState.LastPushCount = n
			srcState.LastPush = time.Now().UTC()
		}
		_ = stateWriter.Save(srcState)
		return n, err
	}

	if sourceOnce {
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		_, err := push(ctx)
		return err
	}

	// --watch mode: long-running fsnotify watcher.
	w, err := watcher.New(watcher.Config{
		CookiesPath: cfg.Chrome.DBPath,
		Push:        push,
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
) (int, error) {
	all, err := chrome.ReadCookiesForHost(cfg.Chrome.DBPath, "%", key)
	if err != nil {
		return 0, fmt.Errorf("read cookies: %w", err)
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

	result := map[string]any{
		"cookies_read":    totalRead,
		"cookies_blocked": totalDropped,
		"cookies_passing": len(all),
		"dry_run":         dryRun,
		"sink_url":        cfg.Sink.URL,
		"posted":          false,
	}

	if dryRun || len(all) == 0 {
		_ = emit(result, fmt.Sprintf("agentcookie source: %d cookies after blocklist (dry-run=%v)\n", len(all), dryRun))
		return 0, nil
	}

	envelope := protocol.SyncEnvelope{
		ProtocolVersion: protocol.Version,
		SourceHostname:  pairing.LocalHostname(),
		Sequence:        time.Now().Unix(),
		Cookies:         all,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return 0, fmt.Errorf("marshal envelope: %w", err)
	}
	sealed, err := transport.SealWithSecret(payload, secret)
	if err != nil {
		return 0, fmt.Errorf("seal payload: %w", err)
	}

	postCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(postCtx, "POST", cfg.Sink.URL, bytes.NewReader(sealed))
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST to sink %s: %w", cfg.Sink.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	result["posted"] = resp.StatusCode == http.StatusOK
	result["sink_response"] = string(body)
	result["sink_status"] = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("sink returned %d: %s", resp.StatusCode, string(body))
	}
	_ = emit(result, fmt.Sprintf("agentcookie source: posted %d cookies, sink replied: %s\n", len(all), string(body)))
	return len(all), nil
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
