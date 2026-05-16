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
	"github.com/mvanhorn/agentcookie/internal/transport"
)

var (
	sourceOnce    bool
	sourceVerbose bool
	sourceDryRun  bool
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Read allowlisted cookies from local Chrome and push them to the configured sink",
	Long: `On the source machine (your laptop), 'agentcookie source --once' performs
one read+push cycle: load source.yaml, load allowlist.yaml, decrypt cookies
for each allowlisted domain via the macOS Keychain Safe Storage path, and
POST an AES-GCM-encrypted JSON payload to the configured sink URL.

Long-lived watch mode (fsnotify on Chrome's cookies SQLite) is planned for
U6 of the AgentCookie roadmap; today this command requires --once.`,
	RunE: runSource,
}

func init() {
	sourceCmd.Flags().BoolVar(&sourceOnce, "once", false, "do a single read+push pass and exit (required until U6 ships long-lived mode)")
	sourceCmd.Flags().BoolVar(&sourceVerbose, "verbose", false, "log per-pattern decisions to stderr")
	sourceCmd.Flags().BoolVar(&sourceDryRun, "dry-run", false, "read + filter but do not contact the sink")
}

func runSource(cmd *cobra.Command, args []string) error {
	if !sourceOnce {
		return fmt.Errorf("long-lived watch mode is not yet implemented; pass --once for a single pass (U6 will lift this)")
	}

	cfg, err := config.LoadSource(common.ConfigDir)
	if err != nil {
		return err
	}
	allow, err := config.LoadAllowlist(common.ConfigDir)
	if err != nil {
		return err
	}
	if len(allow.Domains) == 0 {
		return fmt.Errorf("allowlist is empty; add at least one domain pattern to %s/allowlist.yaml", common.ConfigDir)
	}

	password, err := chrome.SafeStoragePassword()
	if err != nil {
		return fmt.Errorf("read Chrome Safe Storage from Keychain: %w", err)
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return err
	}

	var all []chrome.Cookie
	byPattern := map[string]int{}
	for _, entry := range allow.Domains {
		cookies, err := chrome.ReadCookiesForHost(cfg.Chrome.DBPath, entry.Pattern, key)
		if err != nil {
			return fmt.Errorf("read pattern %q: %w", entry.Pattern, err)
		}
		byPattern[entry.Pattern] = len(cookies)
		all = append(all, cookies...)
		if sourceVerbose {
			fmt.Fprintf(os.Stderr, "agentcookie source: %s -> %d cookies\n", entry.Pattern, len(cookies))
		}
	}

	result := map[string]any{
		"cookies_read":   len(all),
		"by_pattern":     byPattern,
		"dry_run":        sourceDryRun,
		"sink_url":       cfg.Sink.URL,
		"posted":         false,
		"sink_response":  "",
	}

	if sourceDryRun || len(all) == 0 {
		return emit(result, fmt.Sprintf("agentcookie source: %d cookies across %d patterns (dry-run=%v)\n", len(all), len(byPattern), sourceDryRun))
	}

	payload, err := json.Marshal(all)
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}
	sealed, err := transport.SealWithSecret(payload, cfg.Security.SharedSecret)
	if err != nil {
		return fmt.Errorf("seal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Sink.URL, bytes.NewReader(sealed))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST to sink %s: %w", cfg.Sink.URL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	result["posted"] = resp.StatusCode == http.StatusOK
	result["sink_response"] = string(body)
	result["sink_status"] = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sink returned %d: %s", resp.StatusCode, string(body))
	}
	return emit(result, fmt.Sprintf("agentcookie source: posted %d cookies, sink replied: %s\n", len(all), string(body)))
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
