package source

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromedirsync"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type chromiumFileReader struct {
	browser chrome.Browser
	paths   WatchPaths
	opts    Options
	deps    Dependencies
	key     []byte
}

func NewChromiumFile(cfg *config.SourceConfig, opts Options, deps Dependencies) (Reader, error) {
	browser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return nil, err
	}
	if cfg.Chrome.DBPath == "" {
		return nil, fmt.Errorf("source Cookies path is required")
	}
	if deps.Password == nil {
		return nil, fmt.Errorf("source credential provider is required")
	}
	password, err := deps.Password(browser)
	if err != nil {
		return nil, err
	}
	key, err := chrome.DeriveAESKey(password)
	if err != nil {
		return nil, err
	}
	return NewChromiumFileWithKey(cfg, opts, deps, key)
}

// NewChromiumFileWithKey supports callers that cache one derived key for the
// daemon lifetime. It never consults any credential store.
func NewChromiumFileWithKey(cfg *config.SourceConfig, opts Options, deps Dependencies, key []byte) (Reader, error) {
	browser, err := chrome.LookupBrowser(cfg.Browser.Name)
	if err != nil {
		return nil, err
	}
	if cfg.Chrome.DBPath == "" {
		return nil, fmt.Errorf("source Cookies path is required")
	}
	if deps.ReadCookies == nil {
		deps.ReadCookies = chrome.ReadCookiesForHost
	}
	if deps.Pack == nil {
		deps.Pack = chromedirsync.Pack
	}
	return &chromiumFileReader{browser: browser, paths: ChromiumPaths(cfg.Chrome.DBPath), opts: opts, deps: deps, key: slices.Clone(key)}, nil
}
func (r *chromiumFileReader) Label() string           { return r.browser.Name + " (file)" }
func (r *chromiumFileReader) Family() browsers.Family { return browsers.Chromium }
func (r *chromiumFileReader) Capabilities() Caps {
	return Caps{Surfaces: []protocol.Surface{protocol.Cookies, protocol.LocalStorage, protocol.IndexedDB}}
}
func (r *chromiumFileReader) WatchPaths() WatchPaths { return r.paths }
func (r *chromiumFileReader) Close() error           { clear(r.key); return nil }
func (r *chromiumFileReader) Diagnose(ctx context.Context) []Check {
	_, err := r.Read(ctx, &config.Blocklist{Version: 1})
	if err != nil {
		return []Check{{Name: "Source adapter", Severity: "fail", Detail: err.Error()}}
	}
	return []Check{{Name: "Source adapter", Severity: "ok", Detail: r.Label()}}
}
func (r *chromiumFileReader) Read(ctx context.Context, policy *config.Blocklist) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	all, err := r.deps.ReadCookies(r.paths.CookiesPath, "%", r.key)
	if err != nil {
		return nil, fmt.Errorf("read cookies: %w", err)
	}
	snap := newSnapshot(all, policy, r.opts)
	for _, lane := range []struct {
		surface protocol.Surface
		enabled bool
		path    string
		cap     int64
	}{{protocol.LocalStorage, r.opts.LocalStorage, r.paths.LocalStorageDir, 0}, {protocol.IndexedDB, r.opts.IndexedDB, r.paths.IndexedDBDir, 5 * 1024 * 1024}} {
		if !lane.enabled {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, skipped, err := r.deps.Pack(lane.path, lane.cap)
		if err != nil {
			snap.Meta.Surfaces[lane.surface] = protocol.SurfaceStatus{State: protocol.Incomplete, Reason: err.Error()}
			if !errors.Is(err, chromedirsync.ErrSourceMissing) {
				snap.Warnings = append(snap.Warnings, fmt.Sprintf("%s pack failed (%v); continuing without it", lane.surface, err))
			}
			continue
		}
		snap.Meta.Surfaces[lane.surface] = protocol.SurfaceStatus{State: protocol.Complete}
		if lane.surface == protocol.LocalStorage {
			snap.LocalStorageTarball = data
		} else {
			snap.IndexedDBTarball = data
			slices.Sort(skipped)
			snap.IndexedDBSkipped = skipped
			if len(skipped) > 0 {
				snap.Meta.Surfaces[lane.surface] = protocol.SurfaceStatus{State: protocol.Incomplete, Reason: "databases over size limit"}
			}
		}
	}
	return snap, nil
}
