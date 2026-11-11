package cli

import (
	"context"
	"os"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cdpsource"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/source"
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
	snap, err := readConfiguredSnapshot(ctx, cfg, blocklist, key, skipDBSC, now, false)
	if err != nil {
		return nil, readStats{}, err
	}
	return snap.Cookies, convertReadStats(snap.Stats), nil
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
	cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: dbPath}}
	return readConfiguredCookies(context.Background(), cfg, blocklist, key, skipDBSC, now)
}

func convertReadStats(st source.Stats) readStats {
	return readStats{totalRead: st.TotalRead, totalDropped: st.TotalDropped, droppedHosts: st.DroppedHosts, dbsc: dbscSummary{warned: len(st.DBSC.Warned), skipped: len(st.DBSC.Skipped), sample: dbscSampleReasons(st.DBSC)}}
}

func newSourceReader(cfg *config.SourceConfig, key []byte, skipDBSC bool, now time.Time, storage bool) (source.Reader, error) {
	opts := source.Options{SkipDBSC: skipDBSC, Now: func() time.Time { return now }, LocalStorage: storage && !skipLocalStorage(), IndexedDB: storage && os.Getenv("AGENTCOOKIE_SYNC_INDEXEDDB") == "1"}
	deps := source.Dependencies{ReadCDP: readCDPSource}
	if cfg.CDPSource.Enabled {
		return source.NewCDP(cfg.CDPSource.Endpoint, opts, deps)
	}
	return source.NewChromiumFileWithKey(cfg, opts, deps, key)
}
func readConfiguredSnapshot(ctx context.Context, cfg *config.SourceConfig, policy *config.Blocklist, key []byte, skipDBSC bool, now time.Time, storage bool) (*source.Snapshot, error) {
	reader, err := newSourceReader(cfg, key, skipDBSC, now, storage)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader.Read(ctx, policy)
}
