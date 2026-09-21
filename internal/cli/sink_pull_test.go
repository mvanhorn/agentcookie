package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

func TestNormalizePullURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"mac.tailnet", "http://mac.tailnet:9998/pull"},
		{"mac.tailnet:9998", "http://mac.tailnet:9998/pull"},
		{"http://mac.tailnet:9998", "http://mac.tailnet:9998/pull"},
		{"http://mac.tailnet:9998/", "http://mac.tailnet:9998/pull"},
		{"http://mac.tailnet:9998/pull", "http://mac.tailnet:9998/pull"},
		{"https://mac.tailnet.ts.net:9998/pull", "https://mac.tailnet.ts.net:9998/pull"},
		{"  100.64.0.1:9998  ", "http://100.64.0.1:9998/pull"},
	}
	for _, tc := range cases {
		got, err := normalizePullURL(tc.in)
		if err != nil {
			t.Errorf("normalizePullURL(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizePullURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizePullURLRejectsEmpty(t *testing.T) {
	if _, err := normalizePullURL("  "); err == nil {
		t.Fatal("expected error for empty --pull-from")
	}
}

func TestSinkPollAppliesEnvelopeAndSkipsReplay(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
domains: []
`)
	restore := SetResolveSinkURLForTesting(func(_ context.Context, rawURL string) (string, error) {
		return rawURL, nil
	})
	defer restore()

	payload, err := json.Marshal(protocol.SyncEnvelope{
		ProtocolVersion: protocol.Version,
		SourceHostname:  "source-test",
		Sequence:        7,
		Cookies: []chrome.Cookie{
			{HostKey: ".allowed.com", Name: "sid", Value: "abc", Path: "/"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := transport.SealWithSecret(payload, fx.secret)
	if err != nil {
		t.Fatal(err)
	}

	hits := 0
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/pull" {
			http.NotFound(w, r)
			return
		}
		if err := transport.VerifyRequest(r, fx.secret, time.Now(), 0); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sealed)
	}))
	defer src.Close()

	pullURL, err := normalizePullURL(src.URL)
	if err != nil {
		t.Fatal(err)
	}

	apply := func(b []byte) envelopeApplyResult { return fx.applySealed(b) }
	if err := pollSourceOnce(context.Background(), pullURL, fx.secret, apply); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if hits != 1 {
		t.Fatalf("source hits = %d, want 1", hits)
	}
	if fx.sinkState.TotalWrites != 1 {
		t.Fatalf("TotalWrites = %d, want 1 after first poll", fx.sinkState.TotalWrites)
	}
	if got := fx.sidecarHosts(); len(got) != 1 || got[0] != ".allowed.com" {
		t.Fatalf("sidecar hosts = %v, want [.allowed.com]", got)
	}

	if err := pollSourceOnce(context.Background(), pullURL, fx.secret, apply); err != nil {
		t.Fatalf("second poll (replay) should skip, got: %v", err)
	}
	if fx.sinkState.TotalWrites != 1 {
		t.Fatalf("TotalWrites = %d, want 1 after sequence skip", fx.sinkState.TotalWrites)
	}
	if fx.seqTracker.Last("source-test") != 7 {
		t.Errorf("sequence last = %d, want 7", fx.seqTracker.Last("source-test"))
	}
}

func TestSinkPollRejectsUnauthenticatedSource(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	restore := SetResolveSinkURLForTesting(func(_ context.Context, rawURL string) (string, error) {
		return rawURL, nil
	})
	defer restore()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer src.Close()

	err := pollSourceOnce(context.Background(), src.URL+"/pull", fx.secret, fx.applySealed)
	if err == nil {
		t.Fatal("expected error for 401 pull response")
	}
}

func TestSinkPollNoContentIsSkip(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	restore := SetResolveSinkURLForTesting(func(_ context.Context, rawURL string) (string, error) {
		return rawURL, nil
	})
	defer restore()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := transport.VerifyRequest(r, fx.secret, time.Now(), 0); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer src.Close()

	if err := pollSourceOnce(context.Background(), src.URL+"/pull", fx.secret, fx.applySealed); err != nil {
		t.Fatalf("204 should be a no-op, got: %v", err)
	}
	if fx.sinkState.TotalWrites != 0 {
		t.Errorf("TotalWrites = %d, want 0", fx.sinkState.TotalWrites)
	}
}
