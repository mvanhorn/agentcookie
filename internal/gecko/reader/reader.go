// Package reader adapts Gecko's live companion and closed-profile reader to the
// source contract. Neither path imports or calls a credential provider.
package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/gecko/broker"
	"github.com/mvanhorn/agentcookie/internal/gecko/catalog"
	"github.com/mvanhorn/agentcookie/internal/gecko/companion"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Name, Profile, Mode, StateDir, StorageReadMode string
	BiDiPort, CompanionPort                        int
	Containers                                     []string
	ContainerSites                                 map[string][]string
	Now                                            func() time.Time
	Executable                                     string
	ExtraAssets                                    map[string]string
}
type Reader struct {
	mu           sync.Mutex
	opts         Options
	mode, reason string
	server       *companion.Server
	broker       *broker.Client
	catalog      *catalog.Catalog
	catalogPath  string
	last         *source.Snapshot
	dropped      map[string]int
}

func Open(ctx context.Context, o Options) (*Reader, error) {
	if o.Profile == "" || o.StateDir == "" {
		return nil, fmt.Errorf("gecko profile and state directory are required")
	}
	if o.Mode == "" {
		o.Mode = "auto"
	}
	if o.Mode != "auto" && o.Mode != "live" && o.Mode != "file" {
		return nil, fmt.Errorf("invalid Gecko reader mode")
	}
	if o.BiDiPort == 0 {
		o.BiDiPort = 9224
	}
	if o.CompanionPort == 0 {
		o.CompanionPort = 9225
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	path := filepath.Join(o.StateDir, "gecko-catalog", companion.ProfileKey(o.Profile)+".json")
	cat, e := catalog.Load(path)
	if e != nil {
		return nil, e
	}
	r := &Reader{opts: o, mode: "file", catalog: cat, catalogPath: path}
	if o.Mode == "file" {
		r.reason = "file mode: cookies only, never deletion-authoritative"
		return r, nil
	}
	_, markerErr := os.Stat(filepath.Join(o.Profile, "WebDriverBiDiServer.json"))
	if markerErr != nil && o.Mode == "auto" {
		r.reason = "BiDi unavailable; closed-profile cookies only"
		return r, nil
	}
	liveCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	b, be := broker.Ensure(liveCtx, broker.Options{Profile: o.Profile, Endpoint: fmt.Sprintf("ws://127.0.0.1:%d/session", o.BiDiPort), StateDir: o.StateDir, CompanionPort: o.CompanionPort, Executable: o.Executable, ExtraAssets: o.ExtraAssets})
	var c companion.Config
	if be == nil {
		r.broker = b
		c = b.Config()
	} else {
		// The existing companion can reconnect after its broker died, preserving
		// cookies even though the unrecoverable BiDi slot pauses intercepted reads.
		raw, e := os.ReadFile(filepath.Join(o.StateDir, "companion", companion.ProfileKey(o.Profile), "config.json"))
		if e != nil || json.Unmarshal(raw, &c) != nil || c.Port != o.CompanionPort || len(c.Token) != 64 {
			if o.Mode == "live" {
				return nil, be
			}
			r.reason = be.Error()
			return r, nil
		}
		r.reason = broker.ErrLostSession.Error()
	}
	server, e := companion.Listen(c)
	if e != nil {
		return nil, e
	}
	r.server = server
	if b != nil && !b.Installed() {
		if e = b.Install(liveCtx); e != nil {
			server.Close()
			return nil, e
		}
	}
	e = server.Wait(liveCtx)
	if errors.Is(e, companion.ErrProtocolMismatch) && b != nil {
		// Reinstall from this daemon's embedded assets, not the older broker's
		// binary. Keep the session token so its authenticated control API and
		// this already-listening companion server remain valid.
		var upgraded companion.Config
		upgraded, e = companion.Provision(filepath.Join(o.StateDir, "companion", companion.ProfileKey(o.Profile)), c.Port, c.ProfileHint, false, o.ExtraAssets)
		if e == nil && upgraded.Token != c.Token {
			e = fmt.Errorf("companion upgrade changed session credentials")
		}
		if e == nil {
			server.ResetMismatch()
			if e = b.Reinstall(liveCtx); e == nil {
				e = server.Wait(liveCtx)
			}
		}
	}
	if e != nil {
		server.Close()
		if o.Mode == "live" {
			return nil, fmt.Errorf("companion unavailable: %w", e)
		}
		r.server = nil
		r.reason = "companion unavailable; closed-profile read required"
		return r, nil
	}
	r.mode = "live"
	return r, nil
}
func (r *Reader) Label() string           { return r.opts.Name + " (" + r.mode + ")" }
func (r *Reader) Family() browsers.Family { return browsers.Gecko }
func (r *Reader) Capabilities() source.Caps {
	return source.Caps{Live: r.mode == "live", Surfaces: []protocol.Surface{protocol.Cookies}}
}
func (r *Reader) WatchPaths() source.WatchPaths {
	return source.WatchPaths{CookiesPath: filepath.Join(r.opts.Profile, "cookies.sqlite"), ExtraDirs: []string{filepath.Join(r.opts.Profile, "storage", "default")}}
}
func (r *Reader) Read(ctx context.Context, policy *config.Blocklist) (*source.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opts.Mode != "file" {
		reconnect := false
		if r.mode == "file" {
			_, e := os.Stat(filepath.Join(r.opts.Profile, "WebDriverBiDiServer.json"))
			reconnect = e == nil
		} else if r.server != nil {
			connected, _, _ := r.server.Status()
			reconnect = !connected
		}
		if reconnect {
			if r.server != nil {
				r.server.Close()
			}
			if r.broker != nil {
				r.broker.Close()
			}
			next, e := Open(ctx, r.opts)
			if e != nil {
				return nil, e
			}
			r.mode = next.mode
			r.reason = next.reason
			r.server = next.server
			r.broker = next.broker
		}
	}
	if r.mode == "live" && r.broker != nil {
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := r.broker.Check(check)
		cancel()
		if err != nil {
			r.broker.Close()
			r.broker = nil
			r.reason = broker.ErrLostSession.Error()
			if r.opts.StorageReadMode == "request" {
				r.reason = "broker unavailable; request storage mode configured"
			}
		}
	}
	now := r.opts.Now()
	var records []gecko.Record
	var identities []gecko.Identity
	dropped := map[string]int{}
	var scopes []gecko.Origin
	complete := r.mode == "live"
	if r.mode == "live" {
		snap, e := r.server.Snapshot(ctx)
		if e != nil {
			return nil, e
		}
		identities = snap.Identities
		for _, c := range snap.Cookies {
			if n, e := gecko.ContainerID(c.StoreID); e == nil {
				scopes = append(scopes, gecko.Origin{Origin: "https://" + strings.TrimPrefix(c.Domain, "."), Container: n})
			}
			record, e := gecko.MapCookie(c, now)
			if e != nil {
				dropped[e.Error()]++
				continue
			}
			records = append(records, record)
		}
	} else {
		snap, e := gecko.ReadFile(ctx, r.opts.Profile, now)
		if e != nil {
			return nil, e
		}
		records = snap.Cookies
		identities = snap.Identities
		dropped = snap.Dropped
	}
	listingErr := r.catalog.Discover(r.opts.Profile, now)
	if r.server != nil {
	drain:
		for {
			select {
			case event := <-r.server.Events():
				var hint struct {
					Origin  string `json:"origin"`
					Domain  string `json:"domain"`
					StoreID string `json:"storeId"`
				}
				if json.Unmarshal(event.Params, &hint) == nil {
					n, e := gecko.ContainerID(hint.StoreID)
					if e == nil {
						host := hint.Domain
						if hint.Origin != "" {
							if u, e := url.Parse(hint.Origin); e == nil {
								host = u.Hostname()
							}
						}
						r.catalog.Observe(host, n)
					}
				}
			default:
				break drain
			}
		}
	}
	for _, entry := range r.catalog.Entries {
		if entry.Status != "removed" {
			scopes = append(scopes, gecko.Origin{Origin: entry.Origin, Container: entry.Container})
		}
	}
	routes, e := gecko.Route(records, scopes, identities, r.opts.Containers, r.opts.ContainerSites)
	if e != nil {
		return nil, e
	}
	matcher := protocol.NewBlocklistMatcher(policy)
	for site, status := range routes {
		if !matcher.ShouldSyncHost(site) {
			status.State = protocol.Excluded
			status.Reason = "source policy"
			routes[site] = status
		}
	}
	snap := &source.Snapshot{ReaderMode: r.mode, ReaderReason: r.reason, Meta: protocol.SnapshotMeta{Sites: routes, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}, protocol.LocalStorage: {State: protocol.Unsupported, Reason: "storage reads not enabled"}, protocol.IndexedDB: {State: protocol.Unsupported, Reason: "storage reads not enabled"}}, Inventory: protocol.InventoryStatus{Listed: r.catalog.Listed, Unread: r.catalog.Unread()}}, Stats: source.Stats{TotalRead: len(records), DroppedHosts: map[string]int{}}}
	if !complete {
		snap.Meta.Surfaces[protocol.Cookies] = protocol.SurfaceStatus{State: protocol.Incomplete, Reason: "file mode"}
	}
	for _, record := range records {
		status := routes[record.Site]
		if status.State != protocol.Shipped || status.Container != record.Container {
			continue
		}
		if !matcher.ShouldSyncHost(record.Cookie.HostKey) {
			snap.Stats.DroppedHosts[record.Cookie.HostKey]++
			snap.Stats.TotalDropped++
			continue
		}
		snap.Cookies = append(snap.Cookies, record.Cookie)
	}
	snap.DroppedScopes = dropped
	for reason, n := range dropped {
		snap.Stats.TotalRead += n
		snap.Stats.TotalDropped += n
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("%d cookies not representable: %s", n, reason))
	}
	if len(dropped) > 0 {
		snap.Meta.Surfaces[protocol.Cookies] = protocol.SurfaceStatus{State: protocol.Incomplete, Reason: "unrepresentable cookie scopes retained at destination"}
	}
	for _, entry := range r.catalog.Entries {
		if entry.Status == "unreadable" {
			snap.Meta.Inventory.Unreadable++
		}
	}
	if listingErr != nil {
		snap.Warnings = append(snap.Warnings, "origin inventory listing failed")
	}
	if r.reason != "" {
		snap.Warnings = append(snap.Warnings, r.reason)
	}
	if e = r.catalog.Save(r.catalogPath); e != nil {
		return nil, e
	}
	snap.RoutingData = &Capture{Records: records, Origins: scopes, Identities: identities}
	r.last = snap
	r.dropped = dropped
	return snap, nil
}
func (r *Reader) Diagnose(context.Context) []source.Check {
	r.mu.Lock()
	defer r.mu.Unlock()
	severity := "INFO"
	detail := r.mode
	if r.reason != "" {
		severity = "WARN"
		detail += " (" + r.reason + ")"
	}
	if r.server != nil {
		connected, reason, _ := r.server.Status()
		if !connected {
			severity = "WARN"
			detail += "; " + reason
		}
	}
	checks := []source.Check{{Name: "Source adapter", Severity: severity, Detail: detail}, {Name: "Storage catalog", Severity: "INFO", Detail: fmt.Sprintf("%d entries, %d unread", len(r.catalog.Entries), r.catalog.Unread())}, {Name: "DBSC", Severity: "INFO", Detail: "classification not applicable to this source"}, {Name: "Source browser engine", Severity: "INFO", Detail: "Gecko: engine-bound sessions cannot be transferred"}}
	if r.last != nil {
		for site, status := range r.last.Meta.Sites {
			if status.State == protocol.Held {
				checks = append(checks, source.Check{Name: "Held site", Severity: "WARN", Detail: fmt.Sprintf("%s: containers %v", site, status.Containers)})
			}
		}
	}
	return checks
}
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil {
		r.server.Close()
	}
	if r.broker != nil {
		r.broker.Close()
	}
	return nil
}
func (r *Reader) Companion() *companion.Server { return r.server }
func (r *Reader) Broker() *broker.Client       { return r.broker }
func (r *Reader) Catalog() *catalog.Catalog    { return r.catalog }

func (r *Reader) Changes() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server == nil {
		return nil
	}
	return r.server.Notifications()
}
