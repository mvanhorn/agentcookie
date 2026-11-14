package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cdpsource"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// readStats summarizes one read+filter pass for logging by the caller.
type readStats struct {
	totalRead    int
	totalDropped int
	droppedHosts map[string]int
	dbsc         dbscSummary
}

var readCDPSource = cdpsource.Read

// readConfiguredCookies reads cookies from the configured source without
// falling back between CDP, Gecko and Chromium SQLite. CDP profiles never
// invoke browser discovery, Keychain, or SQLite; Gecko profiles read their
// unencrypted cookies.sqlite with no Keychain; Chromium profiles retain the
// legacy path.
func readConfiguredCookies(ctx context.Context, cfg *config.SourceConfig, blocklist *config.Blocklist, key []byte, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats, error) {
	if cfg.CDPSource.Enabled {
		all, err := readCDPSource(ctx, cfg.CDPSource.Endpoint)
		if err != nil {
			return nil, readStats{}, fmt.Errorf("read cookies from cdp source: %w", err)
		}
		cookies, stats := filterCookies(all, blocklist, skipDBSC, now)
		return cookies, stats, nil
	}
	if gecko.IsBrowser(cfg.Browser.Name) {
		all, err := gecko.ReadCookies(cfg.Chrome.DBPath, now)
		if err != nil {
			return nil, readStats{}, fmt.Errorf("read %s cookies: %w", cfg.Browser.Name, err)
		}
		cookies, stats := filterCookies(all, blocklist, skipDBSC, now)
		return cookies, stats, nil
	}
	return readFilteredCookies(cfg.Chrome.DBPath, blocklist, key, skipDBSC, now)
}

// readFilteredCookies reads every cookie from the browser's Cookies DB,
// applies the cookie policy, and runs the DBSC classifier -- the shared read
// pipeline behind both `source` (push to a peer) and `cmux-sync` (local
// loop into cmux). Keeping it in one place is what guarantees the two
// paths filter identically.
//
// It returns the cookies that survive both filters (DBSC "shipped"),
// plus stats for the caller to log however fits its surface. Logging and
// result-map shaping stay with the caller so each command keeps its own
// output voice.
func readFilteredCookies(dbPath string, blocklist *config.Blocklist, key []byte, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats, error) {
	all, err := chrome.ReadCookiesForHost(dbPath, "%", key)
	if err != nil {
		return nil, readStats{}, fmt.Errorf("read cookies: %w", err)
	}
	filtered, stats := filterCookies(all, blocklist, skipDBSC, now)
	return filtered, stats, nil
}

// filterCookies applies the common policy and DBSC classification to cookie
// records regardless of whether they came from Chrome SQLite or a live CDP
// endpoint. The source remains fail-closed when the reader itself fails.
func filterCookies(all []chrome.Cookie, blocklist *config.Blocklist, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats) {
	st := readStats{totalRead: len(all)}

	all, st.droppedHosts = protocol.NewBlocklistMatcher(blocklist).Filter(all)
	for _, n := range st.droppedHosts {
		st.totalDropped += n
	}

	dbscRes := chrome.ClassifyCookies(all, now, skipDBSC)
	all = dbscRes.Shipped
	st.dbsc = dbscSummary{
		warned:  len(dbscRes.Warned),
		skipped: len(dbscRes.Skipped),
		sample:  dbscSampleReasons(dbscRes),
	}
	return all, st
}

// sourcePasswordFor is the Keychain reader seam for the source-reading
// commands; tests stub it to prove a path never reads the Keychain.
var sourcePasswordFor = chrome.SafeStoragePasswordFor

// sourceCookieKey returns the cookie decryption key for the source browser
// that override, or else source.yaml, names.
//
// A Gecko browser has no key: it returns nil without touching the Keychain.
// When override names a Gecko browser other than the configured one, cfg is
// pointed at that browser's own cookies.sqlite, so the read never falls back
// to the configured Chrome cookie file.
func sourceCookieKey(cfg *config.SourceConfig, override string, passwordFor func(chrome.Browser) (string, error)) ([]byte, error) {
	if cfg.CDPSource.Enabled {
		return nil, nil
	}
	override = strings.ToLower(strings.TrimSpace(override))
	if gecko.IsBrowser(override) && !strings.EqualFold(override, strings.TrimSpace(cfg.Browser.Name)) {
		path, err := config.SourceBrowserCookiesPath(override, "")
		if err != nil {
			return nil, err
		}
		cfg.Browser = config.BrowserRef{Name: override}
		cfg.Chrome.DBPath = path
	}
	if override != "" && !gecko.IsBrowser(override) && gecko.IsBrowser(cfg.Browser.Name) {
		return nil, fmt.Errorf("--browser %s: source.yaml selects %s, and a Chromium override of a Gecko source is not supported; change browser.name in source.yaml instead", override, cfg.Browser.Name)
	}
	name := override
	if name == "" {
		name = cfg.Browser.Name
	}
	if gecko.IsBrowser(name) {
		return nil, nil
	}
	b, err := chrome.LookupBrowser(name)
	if err != nil {
		return nil, fmt.Errorf("unsupported browser %q (supported: %s)", name, strings.Join(config.SupportedBrowserNames(), ", "))
	}
	password, err := passwordFor(b)
	if err != nil {
		return nil, err
	}
	return chrome.DeriveAESKey(password)
}
