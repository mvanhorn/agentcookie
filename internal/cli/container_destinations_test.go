package cli

import (
	"context"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
	"net/http"
	"testing"
	"time"
)

func TestNamedDestinationNeverFallsBackToSharedLegacyJar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	raw := "http://legacy-container-fixture/sync"
	forgetCapabilities(raw)
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	posted := false
	previous := http.DefaultTransport
	http.DefaultTransport = muxTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capabilities" {
			posted = true
		}
		http.NotFound(w, r)
	})}
	defer func() { http.DefaultTransport = previous }()
	_, _, err := sendNegotiated(context.Background(), config.SinkTarget{URL: raw, Destination: "work"}, "secret", protocol.SyncEnvelope{}, source.Counters{Dir: t.TempDir()}, false)
	if err == nil || posted {
		t.Fatalf("unsafe fallback err=%v posted=%v", err, posted)
	}
}
func TestDestinationReadinessIgnoresSibling(t *testing.T) {
	initial := syncReply{Generation: 99, Destinations: []destination.Status{{Name: "work", Accepted: 5, Ready: true}, {Name: "personal", Accepted: 99, Ready: false}}}
	got, err := pollSinkApplied(context.Background(), "unused", "unused", "source", initial, "work")
	if err != nil || got.Status != "ok" || got.Generation != 5 || len(got.Destinations) != 1 {
		t.Fatalf("wrong route status %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pollSinkApplied(ctx, "unused", "unused", "source", initial, "missing"); err == nil {
		t.Fatal("missing destination marked ready")
	}
}
func TestDestinationDedupAndHistory(t *testing.T) {
	a := config.SinkTarget{Peer: "mini", URL: "http://mini/sync", Destination: "personal"}
	b := a
	b.Destination = "work"
	log := newSinkDeliveryLog()
	hash, _ := envelopeHash(protocol.SyncEnvelope{})
	now := time.Now()
	log.record(a.Peer, a.URL, hash, now, a.Destination)
	if _, ok := log.unchanged(b.Peer, b.URL, hash, now, b.Destination); ok {
		t.Fatal("sibling suppressed")
	}
	if sinkConfigured([]config.SinkTarget{b}, state.SinkPushState{Peer: a.Peer, URL: a.URL, Destination: a.Destination}) {
		t.Fatal("history carried between accounts")
	}
}

func TestNamedOnlyPullRequiresExplicitScope(t *testing.T) {
	old := sourcePullListen
	sourcePullListen = ""
	defer func() { sourcePullListen = old }()
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox"}, Sinks: []config.SinkTarget{{Destination: "personal", Containers: []string{"Personal"}}, {Destination: "work", Containers: []string{"Work"}}}}
	if defaultPullRouteEnabled(cfg) {
		t.Fatal("implicit default pull authorized")
	}
	prefs := destinationStoragePreferences(cfg)
	if len(prefs) != 2 {
		t.Fatal(prefs)
	}
	sourcePullListen = "127.0.0.1:29998"
	if !defaultPullRouteEnabled(cfg) {
		t.Fatal("explicit pull route disabled")
	}
	prefs = destinationStoragePreferences(cfg)
	if len(prefs) != 3 || prefs[2] != nil {
		t.Fatal("explicit default scope not gathered")
	}
}

func TestDestinationDedupKeysCannotAliasPeerNames(t *testing.T) {
	if sinkDeliveryKey("mini/work", "http://mini/sync") == sinkDeliveryKey("mini", "http://mini/sync", "work") {
		t.Fatal("destination aliases peer")
	}
	if sinkDeliveryKey("a\x00b", "c") == sinkDeliveryKey("a", "b\x00c") {
		t.Fatal("unescaped identity components collide")
	}
}
