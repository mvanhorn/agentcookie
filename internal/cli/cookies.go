package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/pkg/sidecar"
)

var cookiesDomain string

var cookiesCmd = &cobra.Command{
	Use:   "cookies",
	Short: "Print synced cookies for a domain (keychain-free, from the local sidecar)",
	Long: `cookies reads agentcookie's local plaintext sidecar and prints the synced
cookies for a domain, so any tool can consume a logged-in session without
touching the macOS Keychain.

This is the supported, universal consumption path: shell out to it from a
CLI's auth step (the way CLIs already shell out to press-auth) instead of
importing agentcookie. Output is a Cookie header by default, or a JSON array
with --json.

  agentcookie cookies --domain .amazon.com
  eval "$(agentcookie cookies --domain .amazon.com)"   # not how you'd use it; see --json for tooling`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cookiesDomain == "" {
			return fmt.Errorf("cookies: --domain is required (e.g. --domain .amazon.com)")
		}
		path, err := sidecar.DefaultPath()
		if err != nil {
			return fmt.Errorf("cookies: resolve sidecar path: %w", err)
		}
		// Enforce the same blocklist the sink applies, so a blocked domain
		// never leaks out through this door.
		bl, _ := config.LoadBlocklist(common.ConfigDir)
		matcher := protocol.NewBlocklistMatcher(bl)

		cookies, err := collectDomainCookies(path, cookiesDomain, matcher)
		if err != nil {
			return fmt.Errorf("cookies: %w", err)
		}
		return emitCookies(cmd.OutOrStdout(), cookies, common.JSON)
	},
}

// collectDomainCookies reads the sidecar at path and returns the cookies whose
// host matches domain and are not blocked. A missing sidecar is not an error
// for a consumer -- it simply means there is nothing synced yet, so the caller
// can fall through to its own auth path. Empty-value rows are skipped.
func collectDomainCookies(path, domain string, matcher *protocol.BlocklistMatcher) ([]sidecar.Cookie, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sidecar %s: %w", path, err)
	}
	// ReadSidecar transparently unseals agc1: values when sealing is enabled;
	// on a plaintext sidecar (the headless-sink default) it never touches the
	// Keychain. A read error (e.g. sealed values with no master key) surfaces
	// here so the consumer fails loud rather than authenticating with garbage.
	all, err := sidecar.ReadSidecar(path)
	if err != nil {
		return nil, fmt.Errorf("read sidecar: %w", err)
	}
	bare := strings.TrimPrefix(domain, ".")
	var matched []sidecar.Cookie
	for _, c := range all {
		if c.Value == "" {
			continue
		}
		if !hostMatchesDomain(c.HostKey, bare) {
			continue
		}
		if matcher != nil && matcher.MatchesHost(c.HostKey) {
			continue
		}
		matched = append(matched, c)
	}
	return matched, nil
}

// hostMatchesDomain reports whether a cookie host_key belongs to the requested
// domain. bare is the domain with any leading dot stripped. It matches the
// exact host and any subdomain, but not look-alikes: ".amazon.com" matches
// "amazon.com", ".amazon.com", and "www.amazon.com", but never
// "evilamazon.com".
func hostMatchesDomain(host, bare string) bool {
	host = strings.TrimPrefix(host, ".")
	return host == bare || strings.HasSuffix(host, "."+bare)
}

// emitCookies writes the cookies as a Cookie header (default) or a JSON array.
func emitCookies(w io.Writer, cookies []sidecar.Cookie, asJSON bool) error {
	if asJSON {
		type outCookie struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
			Path   string `json:"path"`
			Secure bool   `json:"secure"`
		}
		out := make([]outCookie, 0, len(cookies))
		for _, c := range cookies {
			out = append(out, outCookie{
				Name:   c.Name,
				Value:  c.Value,
				Domain: c.HostKey,
				Path:   c.Path,
				Secure: c.IsSecure,
			})
		}
		enc := json.NewEncoder(w)
		return enc.Encode(out)
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	_, err := fmt.Fprintln(w, strings.Join(parts, "; "))
	return err
}

func init() {
	cookiesCmd.Flags().StringVar(&cookiesDomain, "domain", "", "cookie domain to fetch, e.g. .amazon.com (required)")
}
