package reader

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"

	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/gecko/companion"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
)

func storageAssets(o *Options) {
	assets := maps.Clone(o.ExtraAssets)
	if assets == nil {
		assets = map[string]string{}
	}
	maps.Copy(assets, storagecodec.CompanionAssets())
	o.ExtraAssets = assets
}
func (r *Reader) openStorage() error {
	if !r.opts.StorageSurfaces.Enabled() {
		return nil
	}
	var err error
	suffix := ""
	if r.opts.StorageSurfaces != nil {
		suffix = fmt.Sprintf("-%t-%t", r.opts.StorageSurfaces.LocalStorage, r.opts.StorageSurfaces.IndexedDB)
	}
	r.storage, err = storagecodec.NewSource(r.opts.StorageReadMode, filepath.Join(r.opts.StateDir, "gecko-storage", companion.ProfileKey(r.opts.Profile)+suffix+".json"))
	if r.storage != nil {
		r.storage.Surfaces = r.opts.StorageSurfaces
	}
	return err
}

// StoragePreferences gathers the union authorized by configured destinations.
// The caller reroutes this raw snapshot to one container per destination; it
// must never pass the entire multi-container union to a single destination.
func (r *Reader) readStorage(ctx context.Context, records []gecko.Record, scopes []gecko.Origin, identities []gecko.Identity, matcher *protocol.BlocklistMatcher, routes map[string]protocol.SiteStatus, snapshot *source.Snapshot) error {
	if r.mode != "live" || r.storage == nil || !r.opts.StorageSurfaces.Enabled() {
		return nil
	}
	requests, err := r.storageRequests(records, scopes, identities, matcher, routes)
	if err != nil {
		return err
	}
	result, err := r.storage.ReadCycle(ctx, r.server, r.broker, requests, r.catalog.Listed)
	if err != nil {
		return err
	}
	snapshot.Storage = result.Records
	maps.Copy(snapshot.Meta.Surfaces, result.Surfaces)
	for _, outcome := range result.Outcomes {
		for key, entry := range r.catalog.Entries {
			if entry.Origin == outcome.Origin && entry.Container == outcome.Container && entry.Representable {
				r.catalog.ReadResult(key, snapshot.Meta.Generation, outcome.Empty, outcome.Err, r.opts.Now())
			}
		}
	}
	snapshot.Meta.Inventory.Unread = r.catalog.Unread()
	snapshot.Meta.Inventory.Unreadable = 0
	for _, entry := range r.catalog.Entries {
		if entry.Status == "unreadable" {
			snapshot.Meta.Inventory.Unreadable++
		}
	}
	if r.opts.StorageReadMode == "request" {
		snapshot.Warnings = append(snapshot.Warnings, "storage_read_mode=request sends authenticated navigations to source sites")
	}
	for surface, status := range result.Surfaces {
		if status.State != protocol.Complete {
			snapshot.Warnings = append(snapshot.Warnings, string(surface)+": "+status.Reason)
		}
	}
	return nil
}

func (r *Reader) storageRequests(records []gecko.Record, scopes []gecko.Origin, identities []gecko.Identity, matcher *protocol.BlocklistMatcher, routes map[string]protocol.SiteStatus) ([]storagecodec.OriginRequest, error) {
	allRoutes := []map[string]protocol.SiteStatus{}
	if r.opts.StoragePreferences == nil {
		allRoutes = append(allRoutes, routes)
	}
	for _, prefs := range r.opts.StoragePreferences {
		if prefs == nil {
			allRoutes = append(allRoutes, routes)
			continue
		}
		route, err := gecko.Route(records, scopes, identities, prefs, r.opts.ContainerSites)
		if err != nil {
			return nil, err
		}
		allRoutes = append(allRoutes, route)
	}
	due := map[string]bool{}
	for _, entry := range r.catalog.Due(r.opts.Now()) {
		due[entry.Key()] = true
	}
	var requests []storagecodec.OriginRequest
	for _, entry := range r.catalog.Entries {
		if entry.Status == "removed" {
			continue
		}
		u, err := url.Parse(entry.Origin)
		if err != nil || !matcher.ShouldSyncHost(u.Hostname()) {
			continue
		}
		site := gecko.Site(u.Hostname())
		selected := false
		for _, route := range allRoutes {
			status := route[site]
			if status.State == protocol.Shipped && status.Container == entry.Container {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		requests = append(requests, storagecodec.OriginRequest{Origin: entry.Origin, Container: entry.Container, Due: due[entry.Key()], Representable: entry.Representable, Reason: entry.Reason})
	}
	return requests, nil
}
