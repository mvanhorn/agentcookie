package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

type destinationMemory struct {
	values map[string]string
}

func (m *destinationMemory) Contexts(context.Context) ([]destination.Context, error) {
	return []destination.Context{{ID: "default", Incarnation: "test-process", Default: true}}, nil
}
func (m *destinationMemory) Read(_ context.Context, c destination.Context, i destination.Item) (string, bool, error) {
	v, ok := m.values[c.Key()+i.ID()]
	return v, ok, nil
}
func (m *destinationMemory) Set(_ context.Context, c destination.Context, i destination.Item) error {
	m.values[c.Key()+i.ID()] = i.Hash
	return nil
}
func (m *destinationMemory) Delete(_ context.Context, c destination.Context, i destination.Item) error {
	delete(m.values, c.Key()+i.ID())
	return nil
}
func TestLivePipelineWireLoginRotateLogoutAndPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains: []\n")
	mem := &destinationMemory{values: map[string]string{}}
	e, err := destination.Open(filepath.Join(dir, "destination"), nil, mem)
	if err != nil {
		t.Fatal(err)
	}
	p := &destinationPipeline{order: []string{"live"}, entries: map[string]*destinationEntry{"live": {engine: e, surfaces: []string{"cookies"}, config: config.DestinationRef{Kind: "live_chrome", Required: true}}}}
	cfg := &config.SinkConfig{}
	seq := protocol.NewSequenceTracker()
	writer := state.NewWriter(filepath.Join(dir, "sink.json"))
	st := &state.SinkState{}
	mu := &sync.Mutex{}
	apply := func(g uint64, value string) syncReply {
		env := protocol.SyncEnvelope{ProtocolVersion: 3, SourceHostname: "fixture", Destination: "live", Sequence: int64(g + 100), Snapshot: &protocol.SnapshotMeta{Generation: g, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}}}}
		if value != "" {
			env.Cookies = []chrome.Cookie{{Name: "session", Value: value, HostKey: "example.test", Path: "/", SameSite: -1}}
		}
		b, _ := json.Marshal(env)
		sealed, _ := transport.SealWithSecret(b, "fixture-key")
		result := applySealedEnvelope(ctx, cfg, "fixture-key", nil, seq, writer, st, mu, sealed, p)
		if result.Status != 200 {
			t.Fatalf("apply %d: %s", result.Status, result.Reply)
		}
		var reply syncReply
		if err := json.Unmarshal([]byte(result.Reply), &reply); err != nil {
			t.Fatal(err)
		}
		if !reply.Destinations[0].Ready {
			t.Fatalf("not ready: %+v", reply)
		}
		return reply
	}
	apply(1, "one")
	if len(mem.values) != 1 {
		t.Fatal("login missing")
	}
	var first string
	for _, v := range mem.values {
		first = v
	}
	apply(2, "two")
	for _, v := range mem.values {
		if v == first {
			t.Fatal("rotation not value verified")
		}
	}
	mem.values["sink-only"] = "keep"
	apply(3, "")
	if len(mem.values) != 1 || mem.values["sink-only"] != "keep" {
		t.Fatal("logout changed unowned data")
	}
	apply(4, "on")
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains:\n - pattern: example.test\n")
	apply(5, "off")
	if len(mem.values) != 1 {
		t.Fatal("policy revoke failed")
	}
	mux := http.NewServeMux()
	p.register(mux, "fixture-key")
	for _, path := range []string{"/capabilities", "/status?source=fixture"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatal("endpoint unauthenticated")
		}
		req = httptest.NewRequest("GET", path, nil)
		transport.SignRequest(req, "fixture-key", time.Now())
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("endpoint %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

type muxTransport struct{ handler http.Handler }

func (t muxTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestNegotiatedSenderV3AndLegacy(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(map[int]string{2: "v2", 3: "v3"}[version], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			forgetCapabilities("http://fixture/sync")
			t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
			var got protocol.SyncEnvelope
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/capabilities" {
					if version == 2 {
						http.NotFound(w, r)
						return
					}
					if transport.VerifyRequest(r, "secret", time.Now(), 0) != nil {
						t.Error("capabilities unsigned")
					}
					writeAuthenticatedJSON(w, r, "secret", sinkCapabilities{ProtocolVersions: []int{1, 2, 3}})
					return
				}
				body, _ := io.ReadAll(r.Body)
				plain, err := transport.OpenWithSecret(body, "secret")
				if err != nil {
					t.Error(err)
				}
				json.Unmarshal(plain, &got)
				io.WriteString(w, `{"status":"ok","generation":2,"destinations":[]}`)
			})
			prev := http.DefaultTransport
			http.DefaultTransport = muxTransport{handler}
			defer func() { http.DefaultTransport = prev }()
			env := protocol.SyncEnvelope{SourceHostname: "source", Cookies: []chrome.Cookie{{HostKey: "example.test", Name: "s", Value: "v", Path: "/"}}, Snapshot: &protocol.SnapshotMeta{Generation: 2}, Storage: &protocol.StorageRecords{Version: 1}, LocalStorageTarball: []byte("legacy")}
			name := ""
			if version == 3 {
				name = "work"
			}
			_, v, err := sendNegotiated(context.Background(), config.SinkTarget{URL: "http://fixture/sync", Destination: name}, "secret", env, source.Counters{Dir: dir}, false)
			if err != nil || v != version || got.ProtocolVersion != version {
				t.Fatalf("send %d %+v %v", v, got, err)
			}
			if version == 3 && (got.Snapshot == nil || got.Destination != "work" || len(got.LocalStorageTarball) != 0) {
				t.Fatal("v3 metadata missing")
			}
			if version == 2 && (got.Snapshot != nil || got.Storage != nil || got.Destination != "" || string(got.LocalStorageTarball) != "legacy") {
				t.Fatal("legacy projection wrong")
			}
		})
	}
}
func TestLivePipelineUnknownDestinationDoesNotAdvanceReplay(t *testing.T) {
	p := &destinationPipeline{entries: map[string]*destinationEntry{}}
	env := protocol.SyncEnvelope{ProtocolVersion: 3, Destination: "unknown"}
	if err := p.validate(env); err == nil || !strings.Contains(err.Error(), "unknown destination") {
		t.Fatal("unknown destination accepted")
	}
}

func TestSourceStateLossRecoversSemanticGeneration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	mem := &destinationMemory{values: map[string]string{}}
	e, _ := destination.Open(filepath.Join(dir, "sink"), nil, mem)
	e.Accept(context.Background(), "source", destination.Snapshot{Generation: 41, Surfaces: map[string]string{"cookies": "complete"}})
	p := &destinationPipeline{order: []string{"live"}, entries: map[string]*destinationEntry{"live": {engine: e, surfaces: []string{"cookies"}}}}
	mux := http.NewServeMux()
	p.register(mux, "secret")
	prev := http.DefaultTransport
	http.DefaultTransport = muxTransport{mux}
	defer func() { http.DefaultTransport = prev }()
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	forgetCapabilities("http://lost/sync")
	c := source.Counters{Dir: filepath.Join(dir, "lost-source-state")}
	g, _ := c.NextGeneration()
	recovered, err := recoverGeneration(context.Background(), &config.SourceConfig{Sink: config.SinkRef{URL: "http://lost/sync"}, Security: config.SecurityRef{SharedSecret: "secret"}}, c, "source", g)
	if err != nil || recovered != 42 {
		t.Fatalf("generation recovery: %d %v", recovered, err)
	}
	if next, err := c.NextGeneration(); err != nil || next != 43 {
		t.Fatalf("not durable %d %v", next, err)
	}
}

func TestV3PullDeliversEmptyRevocationWhileLegacyGets204(t *testing.T) {
	cache := newPullCache()
	env := protocol.SyncEnvelope{ProtocolVersion: 2, SourceHostname: "source", Sequence: 7, Snapshot: &protocol.SnapshotMeta{Generation: 4, AuthoritativeEmpty: true, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}}}}
	data, _ := json.Marshal(env)
	cache.Store(data)
	handler := newPullHandler(cache, func() []string { return []string{"secret"} })
	for _, version := range []string{"", "3"} {
		req := httptest.NewRequest("GET", "http://source/pull", nil)
		req.Header.Set("X-Agentcookie-Protocol-Version", version)
		transport.SignRequest(req, "secret", time.Now())
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if version == "" {
			if rec.Code != 204 {
				t.Fatal("legacy empty behavior changed")
			}
			continue
		}
		if rec.Code != 200 {
			t.Fatal("v3 logout not delivered")
		}
		plain, err := transport.OpenWithSecret(rec.Body.Bytes(), "secret")
		if err != nil {
			t.Fatal(err)
		}
		var got protocol.SyncEnvelope
		json.Unmarshal(plain, &got)
		if got.ProtocolVersion != 3 || got.Snapshot == nil || !got.Snapshot.AuthoritativeEmpty {
			t.Fatal("revocation metadata lost")
		}
	}
}

func TestCapabilityResponseAuthenticationRejectsTamperingAndDoesNotCache(t *testing.T) {
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	for _, mode := range []string{"unsigned", "tampered", "wrong-nonce"} {
		t.Run(mode, func(t *testing.T) {
			raw := "http://authentication-" + mode + "/sync"
			forgetCapabilities(raw)
			calls := 0
			prev := http.DefaultTransport
			http.DefaultTransport = muxTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body := []byte(`{"protocol_versions":[1,2,3]}`)
				nonce := r.Header.Get(transport.HeaderNonce)
				if mode == "wrong-nonce" {
					nonce = "another-request"
				}
				if mode != "unsigned" {
					w.Header().Set(transport.HeaderResponseMAC, transport.ResponseMAC("secret", nonce, body))
				}
				if mode == "tampered" {
					body = []byte(`{"protocol_versions":[1,2]}`)
				}
				w.Write(body)
			})}
			defer func() { http.DefaultTransport = prev }()
			for range 2 {
				if _, err := discoverCapabilities(context.Background(), raw, "secret"); err == nil {
					t.Fatal("unauthenticated capability response accepted")
				}
			}
			if calls != 2 {
				t.Fatal("invalid response cached")
			}
		})
	}
}

func TestStatusRejectsAuthenticatedQuerySubstitution(t *testing.T) {
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	p := &destinationPipeline{entries: map[string]*destinationEntry{}}
	mux := http.NewServeMux()
	p.register(mux, "secret")
	prev := http.DefaultTransport
	http.DefaultTransport = muxTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = "source=another-source"
		mux.ServeHTTP(w, r)
	})}
	defer func() { http.DefaultTransport = prev }()
	if _, err := getSinkStatus(context.Background(), "http://query-substitution/sync", "secret", "source"); err == nil {
		t.Fatal("status for substituted source accepted")
	}
}

func TestDestinationCommandRejectsSubstitutedCiphertext(t *testing.T) {
	p := &destinationPipeline{entries: map[string]*destinationEntry{"live": {}}}
	mux := http.NewServeMux()
	p.register(mux, "secret")
	original := httptest.NewRequest("POST", "/destination", nil)
	transport.SignRequest(original, "secret", time.Now())
	plain, _ := json.Marshal(map[string]any{"name": "live", "action": "repair", "confirmed": true, "nonce": original.Header.Get(transport.HeaderNonce)})
	sealed, _ := transport.SealWithSecret(plain, "secret")
	req := httptest.NewRequest("POST", "/destination", strings.NewReader(string(sealed)))
	transport.SignRequest(req, "secret", time.Now())
	rec := httptest.NewRecorder()
	// The entry has no engine: reaching the action would panic, so this also
	// proves that ciphertext substitution cannot invoke any destination action.
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("substituted body status %d", rec.Code)
	}
}

func TestDestinationBoundRepairAndReplay(t *testing.T) {
	mem := &destinationMemory{values: map[string]string{}}
	e, err := destination.Open(t.TempDir(), nil, mem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Accept(context.Background(), "source", destination.Snapshot{Generation: 1, Surfaces: map[string]string{"cookies": "complete"}}); err != nil {
		t.Fatal(err)
	}
	p := &destinationPipeline{entries: map[string]*destinationEntry{"live": {engine: e}}}
	mux := http.NewServeMux()
	p.register(mux, "secret")
	req := httptest.NewRequest("POST", "/destination", nil)
	if err = transport.SignRequest(req, "secret", time.Now()); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(destinationAction{Name: "live", Action: "repair", Confirmed: true, Nonce: req.Header.Get(transport.HeaderNonce)})
	sealed, err := transport.SealWithSecret(plain, "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{http.StatusOK, http.StatusConflict} {
		req.Body = io.NopCloser(strings.NewReader(string(sealed)))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("repair status %d want %d: %s", rec.Code, want, rec.Body.String())
		}
	}
}
