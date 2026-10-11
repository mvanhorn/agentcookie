package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/pkg/sidecar"
)

var cookiesDomain string

var cookiesCmd = &cobra.Command{
	Use:   "cookies",
	Short: "Print cookies for a domain from the sidecar and discovered Chrome profiles",
	Long: `cookies reads agentcookie's local plaintext sidecar plus any discovered
Chrome profiles and prints the matching cookies for a domain, so any tool can
consume a logged-in session without touching the macOS Keychain directly.

On macOS, discovered Chrome profiles (Default, Profile N, agent chrome-profile
directories) are decrypted via the existing Keychain path, unless sink.yaml
sets skip_chrome_sqlite, in which case only the sidecar is read and Chrome
Safe Storage is never touched. On Linux, only the sidecar is read (Chrome
SQLite decryption requires libsecret, which is not implemented).

This is the supported, universal consumption path: shell out to it from a
CLI's auth step (the way CLIs already shell out to press-auth) instead of
importing agentcookie. Output is a Cookie header by default, or a JSON array
with --json.

  agentcookie cookies --domain .amazon.com
  agentcookie cookies --domain instacart.com --json | jq .`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if cookiesDomain == "" {
			return fmt.Errorf("cookies: --domain is required (e.g. --domain .amazon.com)")
		}
		path, err := sidecar.DefaultPath()
		if err != nil {
			return fmt.Errorf("cookies: resolve sidecar path: %w", err)
		}
		bl, err := loadFreshBlocklist()
		if err != nil {
			return fmt.Errorf("cookies: load blocklist: %w", err)
		}
		matcher := protocol.NewBlocklistMatcher(bl)

		cookies, err := collectDomainCookiesUnion(path, cookiesDomain, matcher)
		if err != nil {
			return fmt.Errorf("cookies: %w", err)
		}
		return emitCookies(cmd.OutOrStdout(), cookies, common.JSON)
	},
}

// collectDomainCookiesUnion reads cookies from both the sidecar and
// discovered Chrome profiles, then unions them. Sidecar cookies take
// priority when a cookie with the same host+name exists in both.
// On Linux, Chrome SQLite profiles are skipped (no decrypt support), and
// they are skipped everywhere when sink.yaml sets skip_chrome_sqlite.
func collectDomainCookiesUnion(sidecarPath, domain string, matcher *protocol.BlocklistMatcher) ([]sidecar.Cookie, error) {
	// Start with sidecar cookies.
	sidecarCookies, err := collectDomainCookiesFromSidecar(sidecarPath, domain, matcher)
	if err != nil {
		return nil, err
	}

	// Build a set of host+name keys for deduplication.
	seen := make(map[string]bool)
	for _, c := range sidecarCookies {
		key := c.HostKey + "\x00" + c.Name
		seen[key] = true
	}

	// On Darwin, also read from discovered Chrome profiles.
	// Include config's CDP.ProfileDir if set.
	if runtime.GOOS == "darwin" {
		profileDir := ""
		sinkCfg, loadErr := config.LoadSink(common.ConfigDir) // nil when absent or invalid
		if sinkCfg != nil {
			profileDir = sinkCfg.CDP.ProfileDir
		}
		// A sink.yaml that exists but fails to load may still set
		// skip_chrome_sqlite, so fail closed rather than reading Safe Storage.
		_, statErr := os.Stat(filepath.Join(common.ConfigDir, "sink.yaml"))
		unloadableSinkConfig := loadErr != nil && statErr == nil
		// skip_chrome_sqlite means no Safe Storage read: sidecar only.
		if !unloadableSinkConfig && safeStorageReadAllowed(sinkCfg) {
			chromeCookies := collectDomainCookiesFromChrome(domain, matcher, seen, profileDir)
			sidecarCookies = append(sidecarCookies, chromeCookies...)
		}
	}

	return sidecarCookies, nil
}

// collectDomainCookiesFromSidecar reads the sidecar at path and returns the
// cookies whose host matches domain and are not blocked. A missing sidecar is
// not an error -- it simply means there is nothing synced yet.
func collectDomainCookiesFromSidecar(path, domain string, matcher *protocol.BlocklistMatcher) ([]sidecar.Cookie, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sidecar %s: %w", path, err)
	}
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
		if matcher != nil && !matcher.ShouldSyncHost(c.HostKey) {
			continue
		}
		matched = append(matched, c)
	}
	return matched, nil
}

// collectDomainCookiesFromChrome reads cookies from discovered Chrome profiles.
// It skips stores that fail to decrypt (e.g., wrong key, locked DB) and only
// returns cookies not already in the seen set (deduplication with sidecar).
// profileDir is passed to DiscoverForConfig to include a configured CDP profile.
// Stores are processed in stable order: browsers sorted alphabetically, then
// stores sorted with Default profile first followed by alphabetical profile names.
func collectDomainCookiesFromChrome(domain string, matcher *protocol.BlocklistMatcher, seen map[string]bool, profileDir string) []sidecar.Cookie {
	var result []sidecar.Cookie
	bare := strings.TrimPrefix(domain, ".")
	hostPattern := "%" + bare

	discovery := chromepaths.DiscoverForConfig(profileDir)

	// Group stores by browser to reuse decryption keys.
	browserStores := make(map[string][]chromepaths.Store)
	for _, store := range discovery.Stores {
		browserStores[store.Browser] = append(browserStores[store.Browser], store)
	}

	// Sort browser names for deterministic iteration order.
	browserNames := make([]string, 0, len(browserStores))
	for name := range browserStores {
		browserNames = append(browserNames, name)
	}
	sort.Strings(browserNames)

	for _, browserName := range browserNames {
		stores := browserStores[browserName]

		// Sort stores: Default first, then alphabetically by profile name,
		// then by CookiesPath as tiebreaker for same-named profiles from
		// different roots (ensures stable first-wins deduplication).
		sort.Slice(stores, func(i, j int) bool {
			if stores[i].IsDefault != stores[j].IsDefault {
				return stores[i].IsDefault
			}
			if stores[i].Profile != stores[j].Profile {
				return stores[i].Profile < stores[j].Profile
			}
			return stores[i].CookiesPath < stores[j].CookiesPath
		})

		key, err := getChromeDecryptKey(browserName)
		if err != nil {
			// Can't decrypt this browser's cookies - skip all its stores.
			continue
		}

		for _, store := range stores {
			cookies, err := readCookiesFromStore(store.CookiesPath, hostPattern, key)
			if err != nil {
				// Skip this store on error (locked, corrupt, etc.)
				continue
			}

			for _, c := range cookies {
				if c.Value == "" {
					continue
				}
				if !hostMatchesDomain(c.HostKey, bare) {
					continue
				}
				if matcher != nil && !matcher.ShouldSyncHost(c.HostKey) {
					continue
				}
				cookieKey := c.HostKey + "\x00" + c.Name
				if seen[cookieKey] {
					continue
				}
				seen[cookieKey] = true
				result = append(result, sidecar.Cookie{
					HostKey:    c.HostKey,
					Name:       c.Name,
					Value:      c.Value,
					Path:       c.Path,
					ExpiresUTC: c.ExpiresUTC,
					IsSecure:   c.IsSecure != 0,
					IsHTTPOnly: c.IsHTTPOnly != 0,
				})
			}
		}
	}

	return result
}

// safeStorageReadAllowed is the single gate for reading Chrome Safe
// Storage to decrypt extra Chrome profiles (#137). skip_chrome_sqlite in
// sinkCfg forbids the read everywhere: the sink daemon, `cookies --domain`,
// and doctor. sinkCfg is nil on a box without a sink config, which allows
// the read.
func safeStorageReadAllowed(sinkCfg *config.SinkConfig) bool {
	return sinkCfg == nil || !sinkCfg.SkipChromeSQLite
}

// chromeDecryptKeyRead is the uncached Chrome Safe Storage read. It is a
// package var so tests can stub the Keychain read.
var chromeDecryptKeyRead = readChromeDecryptKey

// decryptKeyRefusals is the process-lifetime negative cache for
// getChromeDecryptKey, keyed by browser name. It holds only permanent
// refusals (no grant, timeout). A locked keychain is transient and is
// retried on the next call. The mutex is held across the read so
// concurrent /sync handlers make at most one refused read per browser.
var (
	decryptKeyMu       sync.Mutex
	decryptKeyRefusals = map[string]error{}
)

// decryptKeyRefusalLog receives the one-time line logged when a browser's
// refusal is first cached.
var decryptKeyRefusalLog io.Writer = os.Stderr

// getChromeDecryptKey retrieves the AES key for decrypting cookies from the
// specified browser's Keychain entry. A permanent refusal is cached and
// returned without another Keychain read for the rest of the process.
func getChromeDecryptKey(browserName string) ([]byte, error) {
	decryptKeyMu.Lock()
	defer decryptKeyMu.Unlock()
	if err, ok := decryptKeyRefusals[browserName]; ok {
		return nil, err
	}
	key, err := chromeDecryptKeyRead(browserName)
	if err != nil && chrome.IsKeychainAccessError(err) {
		decryptKeyRefusals[browserName] = err
		fmt.Fprintf(decryptKeyRefusalLog, "agentcookie: %s Safe Storage read refused (%v); not retrying for the life of this process. Restart the sink to retry after granting access.\n", browserName, err)
	}
	return key, err
}

// readChromeDecryptKey reads the browser's Safe Storage password from the
// Keychain and derives the cookie AES key from it.
func readChromeDecryptKey(browserName string) ([]byte, error) {
	browser, err := chrome.LookupBrowser(browserName)
	if err != nil {
		return nil, err
	}
	password, err := chrome.SafeStoragePasswordFor(browser)
	if err != nil {
		return nil, err
	}
	return chrome.DeriveAESKey(password)
}

// readCookiesFromStore reads cookies from a Chrome SQLite file.
func readCookiesFromStore(cookiesPath, hostPattern string, key []byte) ([]chrome.Cookie, error) {
	return chrome.ReadCookiesForHost(cookiesPath, hostPattern, key)
}

// collectDomainCookies is the legacy function that reads only from sidecar.
// Kept for backward compatibility with tests.
func collectDomainCookies(path, domain string, matcher *protocol.BlocklistMatcher) ([]sidecar.Cookie, error) {
	return collectDomainCookiesFromSidecar(path, domain, matcher)
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
