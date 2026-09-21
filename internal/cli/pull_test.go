package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

func TestPullHandlerRejectsBadHMAC(t *testing.T) {
	cache := newPullCache()
	cache.Store([]byte(`{"protocol_version":2,"source_hostname":"mac","sequence":1}`))
	h := newPullHandler(cache, func() []string { return []string{"correct-secret"} })

	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	if err := transport.SignRequest(req, "wrong-secret", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%q", rec.Code, rec.Body.String())
	}
}

func TestPullHandlerRejectsMissingAuth(t *testing.T) {
	cache := newPullCache()
	cache.Store([]byte(`{"protocol_version":2}`))
	h := newPullHandler(cache, func() []string { return []string{"correct-secret"} })

	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%q", rec.Code, rec.Body.String())
	}
}

func TestPullHandlerAcceptsGoodHMACAndSeals(t *testing.T) {
	secret := "correct-secret"
	payload := []byte(`{"protocol_version":2,"source_hostname":"mac","sequence":42}`)
	cache := newPullCache()
	cache.Store(payload)
	h := newPullHandler(cache, func() []string { return []string{secret} })

	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	if err := transport.SignRequest(req, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	got, err := transport.OpenWithSecret(rec.Body.Bytes(), secret)
	if err != nil {
		t.Fatalf("open sealed /pull body: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload = %s, want %s", got, payload)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
}

func TestPullHandlerNoContentWhenEmpty(t *testing.T) {
	secret := "correct-secret"
	h := newPullHandler(newPullCache(), func() []string { return []string{secret} })

	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	if err := transport.SignRequest(req, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
}

func TestPullHandlerGETOnly(t *testing.T) {
	h := newPullHandler(newPullCache(), func() []string { return []string{"secret"} })
	req := httptest.NewRequest(http.MethodPost, "/pull", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestPullAuthSecretsIncludesPeerKeysAndLegacy(t *testing.T) {
	dir := t.TempDir()
	pk := &keystore.PeerKey{
		Peer: "muse-box",
		Key:  []byte("paired-peer-key-32-bytes-long!!"),
	}
	if err := keystore.Save(dir, pk); err != nil {
		t.Fatal(err)
	}
	got := pullAuthSecrets(dir, "legacy-shared-secret")
	if len(got) != 2 {
		t.Fatalf("secrets = %d, want 2 (peer + legacy)", len(got))
	}
	seen := map[string]bool{}
	for _, s := range got {
		seen[s] = true
	}
	if !seen[string(pk.Key)] || !seen["legacy-shared-secret"] {
		t.Errorf("secrets = %v, want peer key and legacy", got)
	}
}

func TestResolveSourcePullListenExplicit(t *testing.T) {
	got, err := resolveSourcePullListen(t.Context(), "127.0.0.1:9998")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:9998" {
		t.Errorf("got %q, want 127.0.0.1:9998", got)
	}
}

func TestResolveSourcePullListenRefusesAnyInterface(t *testing.T) {
	if _, err := resolveSourcePullListen(t.Context(), "0.0.0.0:9998"); err == nil {
		t.Fatal("expected error for 0.0.0.0")
	}
}

func TestSourceHelpMentionsPullListen(t *testing.T) {
	if sourceCmd.Flags().Lookup("pull-listen") == nil {
		t.Fatal("source is missing --pull-listen")
	}
	if sinkCmd.Flags().Lookup("pull-from") == nil {
		t.Fatal("sink is missing --pull-from")
	}
	if sinkCmd.Flags().Lookup("pull-interval") == nil {
		t.Fatal("sink is missing --pull-interval")
	}
}

func TestSourcePushPublishesPullCache(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})
	if _, err := fx.push(); err != nil {
		t.Fatalf("push: %v", err)
	}
	raw := pullPayloadCache.Load()
	if len(raw) == 0 {
		t.Fatal("push should publish a pull envelope")
	}
	var env protocol.SyncEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal cached envelope: %v", err)
	}
	if env.Sequence == 0 {
		t.Error("cached envelope missing sequence")
	}
	if len(env.Cookies) == 0 {
		t.Error("cached envelope missing cookies")
	}
}

func TestPullCacheClearedWhenPolicyFiltersEverything(t *testing.T) {
	pullPayloadCache.Clear()
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
domains: []
`)
	if _, err := fx.push(); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	rec := getPull(t, fx.secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed /pull status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	got, err := transport.OpenWithSecret(rec.Body.Bytes(), fx.secret)
	if err != nil {
		t.Fatalf("open seed /pull: %v", err)
	}
	var env protocol.SyncEnvelope
	if err := json.Unmarshal(got, &env); err != nil {
		t.Fatalf("unmarshal seed envelope: %v", err)
	}
	if len(env.Cookies) == 0 {
		t.Fatal("seed /pull should return cookies")
	}

	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: "never-this-host.invalid"
`)
	if _, err := fx.push(); err != nil {
		t.Fatalf("filter-all push: %v", err)
	}
	rec = getPull(t, fx.secret)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("after filter-all /pull status = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
}

func TestPullCacheClearedWhenPolicyLoadFails(t *testing.T) {
	pullPayloadCache.Clear()
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
domains: []
`)
	if _, err := fx.push(); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	if rec := getPull(t, fx.secret); rec.Code != http.StatusOK {
		t.Fatalf("seed /pull status = %d, want 200", rec.Code)
	}

	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains: []
unexpected: true
`)
	if _, err := fx.push(); err == nil {
		t.Fatal("malformed blocklist should fail closed")
	}
	rec := getPull(t, fx.secret)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("after policy load failure /pull status = %d, want 204; body=%q", rec.Code, rec.Body.String())
	}
}

func TestPullCacheStaleClearDoesNotWipeNewerPayload(t *testing.T) {
	cache := newPullCache()
	old := cache.Begin()
	newer := cache.Begin()
	cache.StoreIfCurrent(newer, []byte("new-envelope"))
	cache.ClearIfCurrent(old)
	got := cache.Load()
	if string(got) != "new-envelope" {
		t.Fatalf("stale Clear wiped newer payload: %q", got)
	}
}

func TestPullCacheStaleStoreDoesNotOverwriteNewerPayload(t *testing.T) {
	cache := newPullCache()
	old := cache.Begin()
	newer := cache.Begin()
	cache.StoreIfCurrent(newer, []byte("new-envelope"))
	cache.StoreIfCurrent(old, []byte("old-envelope"))
	got := cache.Load()
	if string(got) != "new-envelope" {
		t.Fatalf("stale Store overwrote newer payload: %q", got)
	}
}

func TestPullCacheConcurrentStaleClearLosesToNewerStore(t *testing.T) {
	cache := newPullCache()
	for i := range 50 {
		cache.Clear()
		old := cache.Begin()
		newer := cache.Begin()
		var wg sync.WaitGroup
		wg.Go(func() {
			cache.ClearIfCurrent(old)
		})
		wg.Go(func() {
			cache.StoreIfCurrent(newer, []byte("fresh"))
		})
		wg.Wait()
		if got := string(cache.Load()); got != "fresh" {
			t.Fatalf("iteration %d: stale Clear won over newer Store: %q", i, got)
		}
	}
}

func TestSourcePushCyclesAreSerialized(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})
	var inFlight atomic.Int32
	var overlapped atomic.Bool
	inner := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		if n > 1 {
			overlapped.Store(true)
		}
		time.Sleep(40 * time.Millisecond)
		return inner.RoundTrip(req)
	})
	t.Cleanup(func() { http.DefaultTransport = inner })

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := fx.push()
			errCh <- err
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	if overlapped.Load() {
		t.Fatal("source push cycles overlapped; cookie/secrets/discovery watchers must serialize complete cycles")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStartWatchPullListenerFailsIfAddrInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listen addr: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	prev := sourcePullListen
	sourcePullListen = addr
	t.Cleanup(func() { sourcePullListen = prev })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	err = startWatchPullListener(ctx, &config.SourceConfig{})
	if err == nil {
		t.Fatal("expected bind error when pull listen address is occupied")
	}
	if !strings.Contains(err.Error(), "pull listen") {
		t.Errorf("error should name the pull listener, got: %v", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error should include %s, got: %v", addr, err)
	}
}

func getPull(t *testing.T, secret string) *httptest.ResponseRecorder {
	t.Helper()
	h := newPullHandler(pullPayloadCache, func() []string { return []string{secret} })
	req := httptest.NewRequest(http.MethodGet, "/pull", nil)
	if err := transport.SignRequest(req, secret, time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
