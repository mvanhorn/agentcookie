package cli

import (
	"context"
	"encoding/json"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func containerString(s string) json.RawMessage {
	v, _ := json.Marshal(map[string]string{"t": "str", "v": s})
	return v
}
func containerCapture(g uint64, personal, work bool) *source.Snapshot {
	complete := protocol.SurfaceStatus{State: protocol.Complete}
	capture := &geckoreader.Capture{Identities: []gecko.Identity{{Name: "Personal", CookieStoreID: "firefox-container-1"}, {Name: "Work", CookieStoreID: "firefox-container-6"}}, Storage: &protocol.StorageRecords{Version: 1}}
	for i, enabled := range []bool{personal, work} {
		if !enabled {
			continue
		}
		n := []uint32{1, 6}[i]
		value := []string{"personal-account", "work-account"}[i]
		capture.Records = append(capture.Records, gecko.Record{Site: "example.test", Container: n, Cookie: chrome.Cookie{HostKey: "example.test", Name: "session", Value: value, Path: "/", SameSite: -1}})
		capture.Origins = append(capture.Origins, gecko.Origin{Origin: "https://example.test", Container: n})
		db := protocol.IDBDatabase{Name: containerString("auth"), Version: 1, Status: complete, Stores: []protocol.IDBStore{{Name: containerString("sessions"), KeyPath: json.RawMessage("null"), Records: []protocol.IDBRecord{{Key: containerString("current"), Value: containerString(value)}}}}}
		capture.Storage.Origins = append(capture.Storage.Origins, protocol.StorageOrigin{Origin: "https://example.test", Container: n, LocalStorageStatus: complete, IndexedDBStatus: complete, LocalStorage: []protocol.LocalStorageItem{{Key: containerString("session"), Value: containerString(value)}}, IndexedDB: []protocol.IDBDatabase{db}})
	}
	return &source.Snapshot{RoutingData: capture, Meta: protocol.SnapshotMeta{Generation: g, Inventory: protocol.InventoryStatus{Listed: true}, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: complete, protocol.LocalStorage: complete, protocol.IndexedDB: complete}}}
}
func containerDesired(env protocol.SyncEnvelope) (destination.Snapshot, int, error) {
	snap, n, err := destination.CookieSnapshot(env)
	if err == nil {
		err = storagecodec.AddRecords(&snap, env.Storage)
	}
	return snap, n, err
}

// Scenario 6b drives actual encrypted sender negotiation, replay tracking,
// routed cookie/LS/IDB snapshots, persistent engines and ownership reconciliation.
// Only the final browser API is synthetic; the browser counterpart is gated.
func TestContainerScenario6bWireIsolationLogoutReassignment(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "personal-first", true: "work-first"}[reverse], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			withConfigDir(t, dir)
			writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains: []\n")
			previousConverter := convertDestinationSnapshot
			convertDestinationSnapshot = containerDesired
			defer func() { convertDestinationSnapshot = previousConverter }()
			p := &destinationPipeline{entries: map[string]*destinationEntry{}, order: []string{"personal", "work"}}
			jars := map[string]*destinationMemory{}
			for _, name := range p.order {
				m := &destinationMemory{values: map[string]string{"sink-only": "keep"}}
				e, err := destination.Open(filepath.Join(dir, "destinations", name), nil, m)
				if err != nil {
					t.Fatal(err)
				}
				jars[name] = m
				p.entries[name] = &destinationEntry{engine: e, surfaces: []string{"cookies", "local_storage", "indexed_db"}, config: config.DestinationRef{Name: name, Kind: "live_chrome"}}
			}
			seq := protocol.NewSequenceTracker()
			cfg := &config.SinkConfig{}
			st := &state.SinkState{}
			writer := state.NewWriter(filepath.Join(dir, "sink-state.json"))
			mu := &sync.Mutex{}
			var received []protocol.SyncEnvelope
			var sealedBodies [][]byte
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/capabilities" {
					writeAuthenticatedJSON(w, r, "fixture", p.capabilities())
					return
				}
				sealed, _ := io.ReadAll(r.Body)
				plain, err := transport.OpenWithSecret(sealed, "fixture")
				if err != nil {
					t.Error(err)
					http.Error(w, "bad", 400)
					return
				}
				var env protocol.SyncEnvelope
				if err = json.Unmarshal(plain, &env); err != nil {
					t.Error(err)
				}
				received = append(received, env)
				sealedBodies = append(sealedBodies, sealed)
				result := applySealedEnvelope(ctx, cfg, "fixture", nil, seq, writer, st, mu, sealed, p)
				w.WriteHeader(result.Status)
				_, _ = io.WriteString(w, result.Reply)
			})
			old := http.DefaultTransport
			http.DefaultTransport = muxTransport{handler}
			defer func() { http.DefaultTransport = old }()
			raw := "http://container-scenario/sync"
			forgetCapabilities(raw)
			t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
			counters := source.Counters{Dir: filepath.Join(dir, "source")}
			sourceCfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox"}}
			targets := []config.SinkTarget{{URL: raw, Destination: "personal", Containers: []string{"Personal"}}, {URL: raw, Destination: "work", Containers: []string{"Work"}}}
			if reverse {
				targets[0], targets[1] = targets[1], targets[0]
			}
			send := func(snap *source.Snapshot) {
				t.Helper()
				for _, target := range targets {
					env, err := envelopeForDestination(protocol.SyncEnvelope{SourceHostname: "fixture-source", Snapshot: &snap.Meta}, snap, sourceCfg, nil, target)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err = sendNegotiated(ctx, target, "fixture", env, counters, false); err != nil {
						t.Fatal(err)
					}
					desired, _, err := containerDesired(env)
					if err != nil {
						t.Fatal(err)
					}
					jar := jars[target.Destination].values
					if len(jar) != len(desired.Items)+1 {
						t.Fatalf("%s ownership mismatch got=%d want=%d", target.Destination, len(jar), len(desired.Items)+1)
					}
					for _, item := range desired.Items {
						key := (destination.Context{ID: "default", Incarnation: "test-process", Default: true}).Key() + item.ID()
						if jar[key] != item.Hash {
							t.Fatalf("%s leaked or missing %s", target.Destination, item.Surface)
						}
					}
					if jar["sink-only"] != "keep" {
						t.Fatal("unowned value deleted")
					}
				}
			}
			send(containerCapture(7, true, true))
			send(containerCapture(8, false, true))
			for i := range targets {
				if targets[i].Destination == "personal" {
					targets[i].Containers = []string{"Work"}
				} else {
					targets[i].Containers = []string{"Personal"}
				}
			}
			send(containerCapture(9, false, true))
			for i, env := range received {
				if env.Snapshot.Generation != uint64(7+i/2) {
					t.Fatalf("generation changed within cycle: %+v", received)
				}
				if i > 0 && env.Sequence <= received[i-1].Sequence {
					t.Fatal("nonserial transport sequences")
				}
			}
			for _, body := range sealedBodies {
				result := applySealedEnvelope(ctx, cfg, "fixture", nil, seq, writer, st, mu, body, p)
				if result.Status != 409 {
					t.Fatalf("replay accepted: %+v", result)
				}
			}
			for _, name := range p.order {
				if _, err := destination.Open(filepath.Join(dir, "destinations", name), nil, jars[name]); err != nil {
					t.Fatalf("durable %s reopen: %v", name, err)
				}
			}
		})
	}
}

func TestContainerPushReadsOnceAndHashesEachRoute(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	withConfigDir(t, dir)
	withSourceSkipSecrets(t, true)
	previousReader := readSnapshotForPush
	defer func() { readSnapshotForPush = previousReader }()
	reads := 0
	readSnapshotForPush = func(context.Context, *config.SourceConfig, *config.Blocklist, []byte, bool, time.Time, bool) (*source.Snapshot, error) {
		reads++
		return containerCapture(0, true, true), nil
	}
	previousLog := sinkDeliveries
	sinkDeliveries = newSinkDeliveryLog()
	defer func() { sinkDeliveries = previousLog }()
	raw := "http://single-container-capture/sync"
	forgetCapabilities(raw)
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	var mu sync.Mutex
	var envelopes []protocol.SyncEnvelope
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/capabilities" {
			writeAuthenticatedJSON(w, r, "secret", sinkCapabilities{ProtocolVersions: []int{3}})
			return
		}
		if r.URL.Path == "/status" {
			writeAuthenticatedJSON(w, r, "secret", syncReply{Status: "ok", SourceHostname: r.URL.Query().Get("source")})
			return
		}
		body, _ := io.ReadAll(r.Body)
		plain, err := transport.OpenWithSecret(body, "secret")
		if err != nil {
			t.Error(err)
		}
		var env protocol.SyncEnvelope
		if err = json.Unmarshal(plain, &env); err != nil {
			t.Error(err)
		}
		mu.Lock()
		envelopes = append(envelopes, env)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	old := http.DefaultTransport
	http.DefaultTransport = muxTransport{handler}
	defer func() { http.DefaultTransport = old }()
	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "firefox"}, Security: config.SecurityRef{SharedSecret: "secret"}, Sinks: []config.SinkTarget{{URL: raw, Destination: "personal", Containers: []string{"Personal"}}, {URL: raw, Destination: "work", Containers: []string{"Work"}}}}
	results, _, _, err := pushOnce(context.Background(), cfg, nil, nil, false, false, false, 0, &state.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if reads != 1 || len(envelopes) != 2 {
		t.Fatalf("reads=%d envelopes=%d", reads, len(envelopes))
	}
	if envelopes[0].Snapshot.Generation != envelopes[1].Snapshot.Generation || envelopes[0].Sequence >= envelopes[1].Sequence {
		t.Fatal("generation/sequence fanout broken")
	}
	for _, env := range envelopes {
		if len(env.Cookies) != 1 || len(env.Storage.Origins) != 1 {
			t.Fatal("route union leaked")
		}
		if env.Cookies[0].Value != env.Destination+"-account" {
			t.Fatal("cookie account leaked")
		}
	}
	results, _, _, err = pushOnce(context.Background(), cfg, nil, nil, false, false, false, 0, &state.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 || len(envelopes) != 2 {
		t.Fatal("unchanged route resent or reread per destination")
	}
	for _, r := range results {
		if !r.Unchanged {
			t.Fatalf("route not independently unchanged: %+v", r)
		}
	}
	cfg.Sinks[0].Containers = []string{"Work"}
	results, _, _, err = pushOnce(context.Background(), cfg, nil, nil, false, false, false, 0, &state.SourceState{})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 3 || len(envelopes) != 3 || envelopes[2].Destination != "personal" || envelopes[2].Cookies[0].Value != "work-account" {
		t.Fatal("route reassignment not independently resent")
	}
	if !results[1].Unchanged {
		t.Fatal("unchanged sibling resent")
	}
}
