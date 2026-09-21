package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
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
