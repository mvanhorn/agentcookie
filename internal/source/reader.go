// Package source provides source-browser readers without transport, discovery,
// secrets, or destination side effects. All credentials are injected.
package source

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type Reader interface {
	Label() string
	Family() browsers.Family
	Capabilities() Caps
	Read(context.Context, *config.Blocklist) (*Snapshot, error)
	WatchPaths() WatchPaths
	Diagnose(context.Context) []Check
	Close() error
}
type Caps struct {
	Live     bool
	Surfaces []protocol.Surface
}
type WatchPaths struct {
	CookiesPath, LocalStorageDir, IndexedDBDir string
	ExtraDirs                                  []string
}
type Check struct{ Name, Severity, Detail string }
type Stats struct {
	TotalRead, TotalDropped int
	DroppedHosts            map[string]int
	DBSC                    chrome.DBSCResult
}
type Snapshot struct {
	Cookies                               []chrome.Cookie
	Meta                                  protocol.SnapshotMeta
	Storage                               *protocol.StorageRecords
	LocalStorageTarball, IndexedDBTarball []byte
	IndexedDBSkipped                      []string
	Stats                                 Stats
	Warnings                              []string
}
type Options struct {
	SkipDBSC                bool
	LocalStorage, IndexedDB bool
	Now                     func() time.Time
}

// Dependencies requires an explicit credential provider for file readers.
// A nil provider is an error, never a fallback to the user's Keychain.
type Dependencies struct {
	Password    func(chrome.Browser) (string, error)
	ReadCDP     func(context.Context, string) ([]chrome.Cookie, error)
	ReadCookies func(string, string, []byte) ([]chrome.Cookie, error)
	Pack        func(string, int64) ([]byte, []string, error)
}

func Filter(all []chrome.Cookie, policy *config.Blocklist, skipDBSC bool, now time.Time) ([]chrome.Cookie, Stats) {
	st := Stats{TotalRead: len(all)}
	all, st.DroppedHosts = protocol.NewBlocklistMatcher(policy).Filter(all)
	for _, n := range st.DroppedHosts {
		st.TotalDropped += n
	}
	st.DBSC = chrome.ClassifyCookies(all, now, skipDBSC)
	return st.DBSC.Shipped, st
}

func newSnapshot(all []chrome.Cookie, policy *config.Blocklist, opts Options) *Snapshot {
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	cookies, stats := Filter(all, policy, opts.SkipDBSC, now)
	return &Snapshot{Cookies: cookies, Stats: stats, Meta: protocol.SnapshotMeta{Surfaces: map[protocol.Surface]protocol.SurfaceStatus{
		protocol.Cookies: {State: protocol.Complete}, protocol.LocalStorage: {State: protocol.Unsupported}, protocol.IndexedDB: {State: protocol.Unsupported},
	}}}
}

// ChromiumPaths follows the selected Cookies database, including explicit
// chrome.db_path overrides, so storage and cookies cannot select two profiles.
func ChromiumPaths(dbPath string) WatchPaths {
	dir := filepath.Dir(dbPath)
	if filepath.Base(dir) == "Network" {
		dir = filepath.Dir(dir)
	}
	return WatchPaths{CookiesPath: dbPath, LocalStorageDir: filepath.Join(dir, "Local Storage", "leveldb"), IndexedDBDir: filepath.Join(dir, "IndexedDB")}
}

func New(cfg *config.SourceConfig, opts Options, deps Dependencies) (Reader, error) {
	if cfg == nil {
		return nil, fmt.Errorf("source config is required")
	}
	if cfg.CDPSource.Enabled {
		return NewCDP(cfg.CDPSource.Endpoint, opts, deps)
	}
	return NewChromiumFile(cfg, opts, deps)
}
