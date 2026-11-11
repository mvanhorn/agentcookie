package reader

import (
	"context"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/gecko/catalog"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

func TestStorageGatherUnionAndActualHostPolicy(t *testing.T) {
	r := &Reader{opts: Options{Now: time.Now, StoragePreferences: [][]string{nil, {"6"}}}, catalog: catalog.New()}
	var scopes []gecko.Origin
	for _, o := range []gecko.Origin{{Origin: "https://login.example.com", Container: 0}, {Origin: "https://login.example.com", Container: 6}, {Origin: "https://blocked.example.com", Container: 6}} {
		e := &catalog.Entry{Origin: o.Origin, Container: o.Container, Status: "active", Representable: true, Present: true}
		r.catalog.Entries[e.Key()] = e
		scopes = append(scopes, o)
	}
	matcher := protocol.NewBlocklistMatcher(&config.Blocklist{Domains: []config.BlocklistEntry{{Pattern: "blocked.example.com"}}})
	routes := map[string]protocol.SiteStatus{"example.com": {State: protocol.Shipped, Container: 0}}
	requests, err := r.storageRequests(nil, scopes, nil, matcher, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("want two containers, blocked subdomain excluded: %+v", requests)
	}
	seen := map[uint32]bool{}
	for _, request := range requests {
		seen[request.Container] = true
		if request.Origin != "https://login.example.com" {
			t.Fatal(request)
		}
	}
	if !seen[0] || !seen[6] {
		t.Fatal(requests)
	}
	r.opts.StoragePreferences = nil
	requests, err = r.storageRequests(nil, scopes, nil, matcher, routes)
	if err != nil || len(requests) != 1 || requests[0].Container != 0 {
		t.Fatal(requests, err)
	}
}

func TestCookieOnlyNeverInvokesStorageRead(t *testing.T) {
	s, _ := storagecodec.NewSource("request", "")
	r := &Reader{mode: "live", opts: Options{StorageSurfaces: &storagecodec.ReadSurfaces{}}, storage: s}
	snap := &source.Snapshot{}
	// All catalog/companion dependencies intentionally absent: disabled reads must
	// return before any inventory planning, authenticated navigation or RPC.
	if err := r.readStorage(context.Background(), nil, nil, nil, nil, nil, snap); err != nil {
		t.Fatal(err)
	}
	if snap.Storage != nil {
		t.Fatal("cookie-only read acquired storage")
	}
	if len(r.Capabilities().Surfaces) != 1 {
		t.Fatal("disabled surface advertised")
	}
}
