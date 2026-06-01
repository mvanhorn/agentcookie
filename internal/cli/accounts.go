package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/mvanhorn/agentcookie/internal/config"
)

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "Turn cookie sync on or off for a domain",
	Long: "accounts is a thin wrapper over blocklist.yaml. By default agentcookie\n" +
		"syncs every cookie; accounts off adds safe exact+subdomain block patterns for a\n" +
		"domain, and accounts on removes those patterns again.",
}

var accountsOffCmd = &cobra.Command{
	Use:   "off <domain>",
	Short: "Stop syncing cookies for a domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccountsToggle(cmd, args[0], false)
	},
}

var accountsOnCmd = &cobra.Command{
	Use:   "on <domain>",
	Short: "Resume syncing cookies for a domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAccountsToggle(cmd, args[0], true)
	},
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List domains currently disabled by the local blocklist",
	RunE: func(cmd *cobra.Command, args []string) error {
		bl, err := config.LoadBlocklist(common.ConfigDir)
		if err != nil {
			return err
		}
		return emitAccountsList(cmd, bl)
	},
}

type accountsToggleResult struct {
	Action     string   `json:"action"`
	Domain     string   `json:"domain"`
	Patterns   []string `json:"patterns"`
	Changed    bool     `json:"changed"`
	ConfigPath string   `json:"config_path"`
}

type accountsListResult struct {
	Domains  []string `json:"domains"`
	Patterns []string `json:"patterns,omitempty"`
}

func runAccountsToggle(cmd *cobra.Command, rawDomain string, enabled bool) error {
	domain, err := normalizeAccountDomain(rawDomain)
	if err != nil {
		return err
	}

	bl, err := config.LoadBlocklist(common.ConfigDir)
	if err != nil {
		return err
	}
	patterns := accountBlockPatterns(domain)
	changed := false
	if enabled {
		changed = removeAccountBlock(bl, domain)
	} else {
		changed = addAccountBlock(bl, domain)
	}
	if changed {
		if err := writeAccountBlocklist(common.ConfigDir, bl); err != nil {
			return err
		}
	}

	action := "off"
	if enabled {
		action = "on"
	}
	return emitAccountsToggle(cmd, accountsToggleResult{
		Action:     action,
		Domain:     domain,
		Patterns:   patterns,
		Changed:    changed,
		ConfigPath: filepath.Join(common.ConfigDir, "blocklist.yaml"),
	})
}

func emitAccountsToggle(cmd *cobra.Command, result accountsToggleResult) error {
	if common.JSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		return enc.Encode(result)
	}
	state := "enabled"
	if result.Action == "off" {
		state = "disabled"
	}
	if result.Changed {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "agentcookie accounts: %s %s\n", result.Domain, state)
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "agentcookie accounts: %s already %s\n", result.Domain, state)
	return err
}

func emitAccountsList(cmd *cobra.Command, bl *config.Blocklist) error {
	result := accountListResult(bl)
	if common.JSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		return enc.Encode(result)
	}
	if len(result.Domains) == 0 && len(result.Patterns) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "agentcookie accounts: all domains enabled (blocklist empty)")
		return err
	}
	for _, domain := range result.Domains {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), domain); err != nil {
			return err
		}
	}
	if len(result.Patterns) > 0 {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "patterns:"); err != nil {
			return err
		}
		for _, p := range result.Patterns {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", p); err != nil {
				return err
			}
		}
	}
	return nil
}

func accountListResult(bl *config.Blocklist) accountsListResult {
	patterns := make(map[string]bool, len(bl.Domains))
	for _, d := range bl.Domains {
		patterns[d.Pattern] = true
	}
	consumed := make(map[string]bool, len(patterns))
	domains := make([]string, 0)
	for pattern := range patterns {
		if strings.HasPrefix(pattern, "%.") {
			domain := strings.TrimPrefix(pattern, "%.")
			if patterns[domain] {
				domains = append(domains, domain)
				consumed[pattern] = true
				consumed[domain] = true
			}
		}
	}
	others := make([]string, 0)
	for pattern := range patterns {
		if !consumed[pattern] {
			others = append(others, pattern)
		}
	}
	sort.Strings(domains)
	sort.Strings(others)
	return accountsListResult{Domains: domains, Patterns: others}
}

func addAccountBlock(bl *config.Blocklist, domain string) bool {
	if bl.Version == 0 {
		bl.Version = 1
	}
	existing := map[string]bool{}
	for _, d := range bl.Domains {
		existing[strings.ToLower(d.Pattern)] = true
	}
	changed := false
	for _, pattern := range accountBlockPatterns(domain) {
		if existing[strings.ToLower(pattern)] {
			continue
		}
		bl.Domains = append(bl.Domains, config.BlocklistEntry{
			Pattern:     pattern,
			Description: "Disabled by agentcookie accounts off " + domain,
		})
		changed = true
	}
	return changed
}

func removeAccountBlock(bl *config.Blocklist, domain string) bool {
	remove := map[string]bool{}
	for _, pattern := range accountBlockPatterns(domain) {
		remove[strings.ToLower(pattern)] = true
	}
	// Also remove the older broad pattern users may have hand-written from the
	// examples. If they ask to turn a domain on, that is the intended reversal.
	remove[strings.ToLower("%"+domain)] = true
	remove[strings.ToLower("."+domain)] = true

	kept := bl.Domains[:0]
	changed := false
	for _, d := range bl.Domains {
		if remove[strings.ToLower(d.Pattern)] {
			changed = true
			continue
		}
		kept = append(kept, d)
	}
	bl.Domains = kept
	return changed
}

func accountBlockPatterns(domain string) []string {
	return []string{domain, "%." + domain}
}

func writeAccountBlocklist(dir string, bl *config.Blocklist) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if bl.Version == 0 {
		bl.Version = 1
	}
	if bl.Domains == nil {
		bl.Domains = []config.BlocklistEntry{}
	}
	path := filepath.Join(dir, "blocklist.yaml")
	f, err := os.CreateTemp(dir, ".blocklist-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp blocklist in %s: %w", dir, err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod temp blocklist: %w", err)
	}
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(bl); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

var accountDomainPattern = regexp.MustCompile("^[a-z0-9][a-z0-9.-]*[a-z0-9]$")

func normalizeAccountDomain(raw string) (string, error) {
	domain := strings.TrimSpace(strings.ToLower(raw))
	if domain == "" {
		return "", fmt.Errorf("accounts: domain is required")
	}
	if strings.Contains(domain, "://") {
		u, err := url.Parse(domain)
		if err != nil {
			return "", fmt.Errorf("accounts: parse domain: %w", err)
		}
		domain = u.Host
	} else if i := strings.IndexByte(domain, '/'); i >= 0 {
		domain = domain[:i]
	}
	if host, _, err := net.SplitHostPort(domain); err == nil {
		domain = host
	}
	domain = strings.TrimPrefix(domain, "*.")
	domain = strings.TrimPrefix(domain, ".")
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || strings.Contains(domain, "..") || !accountDomainPattern.MatchString(domain) {
		return "", fmt.Errorf("accounts: invalid domain %q", raw)
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("accounts: invalid domain %q", raw)
		}
	}
	return domain, nil
}

func init() {
	accountsCmd.AddCommand(accountsOffCmd, accountsOnCmd, accountsListCmd)
}
