package reader

import (
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/gecko/profiles"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"maps"
	"net/url"
	"slices"
)

// Capture preserves alternate identities from one browser read. Subsequent
// destinations may route this capture without another live connection or read.
// It is process-local and never serialized into an envelope.
type Capture struct {
	Profile    profiles.ProfileIdentity
	Records    []gecko.Record
	Origins    []gecko.Origin
	Identities []gecko.Identity
	Storage    *protocol.StorageRecords
}

func Reroute(snapshot *source.Snapshot, policy *config.Blocklist, preferences []string, overrides map[string][]string) (*source.Snapshot, error) {
	return reroute(snapshot, policy, preferences, overrides, false)
}

// RerouteDestination keeps explicit per-sink container selections isolated,
// including after logout leaves another account as the only remaining one.
func RerouteDestination(snapshot *source.Snapshot, policy *config.Blocklist, preferences []string) (*source.Snapshot, error) {
	return reroute(snapshot, policy, preferences, nil, true)
}

func reroute(snapshot *source.Snapshot, policy *config.Blocklist, preferences []string, overrides map[string][]string, strict bool) (*source.Snapshot, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("snapshot required")
	}
	capture, ok := snapshot.RoutingData.(*Capture)
	if !ok {
		return nil, fmt.Errorf("snapshot has no Gecko capture")
	}
	routes, e := gecko.Route(capture.Records, capture.Origins, capture.Identities, preferences, overrides)
	if strict {
		routes, e = gecko.RouteContainers(capture.Records, capture.Origins, capture.Identities, preferences)
	}
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
	out := *snapshot
	out.Meta = snapshot.Meta
	out.Meta.Sites = routes
	out.Meta.Surfaces = maps.Clone(snapshot.Meta.Surfaces)
	out.Stats.DroppedHosts = maps.Clone(snapshot.Stats.DroppedHosts)
	out.DroppedScopes = maps.Clone(snapshot.DroppedScopes)
	out.Warnings = slices.Clone(snapshot.Warnings)
	out.IndexedDBSkipped = slices.Clone(snapshot.IndexedDBSkipped)
	out.LocalStorageTarball = slices.Clone(snapshot.LocalStorageTarball)
	out.IndexedDBTarball = slices.Clone(snapshot.IndexedDBTarball)
	out.Storage = nil
	storage := capture.Storage
	if storage == nil {
		storage = snapshot.Storage
	}
	if storage != nil {
		selected := &protocol.StorageRecords{Version: storage.Version}
		for _, origin := range storage.Origins {
			u, e := url.Parse(origin.Origin)
			if e != nil {
				return nil, e
			}
			route := routes[gecko.Site(u.Hostname())]
			if route.State == protocol.Shipped && route.Container == origin.Container && matcher.ShouldSyncHost(u.Hostname()) {
				selected.Origins = append(selected.Origins, origin)
			}
		}

		// Origin failures belong to their route. Preserve inventory-wide failure,
		// but do not let an unread sibling account block this account's deletes.
		for _, surface := range []protocol.Surface{protocol.LocalStorage, protocol.IndexedDB} {
			status := out.Meta.Surfaces[surface]
			if snapshot.Meta.Inventory.Listed && (status.State == protocol.Complete || status.Reason == "unread or incomplete origins") {
				status = protocol.SurfaceStatus{State: protocol.Complete}
				for _, origin := range selected.Origins {
					originStatus := origin.LocalStorageStatus
					if surface == protocol.IndexedDB {
						originStatus = origin.IndexedDBStatus
					}
					if originStatus.State != protocol.Complete {
						status = protocol.SurfaceStatus{State: protocol.Incomplete, Reason: "unread or incomplete origins"}
						break
					}
				}
				out.Meta.Surfaces[surface] = status
			}
		}
		raw, e := json.Marshal(selected)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &out.Storage); e != nil {
			return nil, e
		}
	}

	out.Cookies = nil
	for _, r := range capture.Records {
		status := routes[r.Site]
		if status.State == protocol.Shipped && status.Container == r.Container && matcher.ShouldSyncHost(r.Cookie.HostKey) {
			out.Cookies = append(out.Cookies, r.Cookie)
		}
	}
	return &out, nil
}
