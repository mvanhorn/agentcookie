package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cdpsource"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
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
// falling back between CDP and SQLite. CDP profiles never invoke browser
// discovery, Keychain, or SQLite; file-based profiles retain the legacy path.
func readConfiguredCookies(ctx context.Context, cfg *config.SourceConfig, blocklist *config.Blocklist, key []byte, skipDBSC bool, now time.Time) ([]chrome.Cookie, readStats, error) {
	if cfg.CDPSource.Enabled {
		all, err := readCDPSource(ctx, cfg.CDPSource.Endpoint)
		if err != nil {
			return nil, readStats{}, fmt.Errorf("read cookies from cdp source: %w", err)
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
