package storagecodec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/destination"
)

func TestRemoteRequiresExactProcessIncarnationBeforeConnect(t *testing.T) {
	const ws = "ws://127.0.0.1:12345/devtools/browser/fixture-uuid"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": ws})
	}))
	defer server.Close()
	r := &remoteRunner{endpoint: server.URL}
	if _, err := r.browserURL(context.Background(), ""); err == nil {
		t.Fatal("missing incarnation accepted")
	}
	if _, err := r.browserURL(context.Background(), destination.Hash([]byte("old-process"))); err == nil {
		t.Fatal("changed process accepted")
	}
	if got, err := r.browserURL(context.Background(), destination.Hash([]byte(ws))); err != nil || got != ws {
		t.Fatal(got, err)
	}
}
func TestRemoteRefusesDiscoveryRedirectAndExternalEndpoint(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com", http.StatusFound)
	}))
	defer s.Close()
	if _, err := (&remoteRunner{endpoint: s.URL}).browserURL(context.Background(), "fixture"); err == nil {
		t.Fatal("redirect accepted")
	}
	if _, err := NewAdapter("http://example.com:9222", fixtureBase{}); err == nil {
		t.Fatal("external endpoint accepted")
	}
}
