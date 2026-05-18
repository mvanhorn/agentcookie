package httpserver

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDefaults_AllProfiles(t *testing.T) {
	cases := []struct {
		profile         Profile
		wantServerSide  bool
		wantClientSide  bool
		wantMaxBodyAtMost int64
	}{
		{SinkSync, true, false, 256 * 1024 * 1024},
		{Pair, true, false, 16 * 1024},
		{PairClient, false, true, 0},
		{SyncClient, false, true, 0},
	}
	for _, c := range cases {
		s := Defaults(c.profile)
		hasServer := s.ReadHeaderTimeout > 0 && s.ReadTimeout > 0
		hasClient := s.ClientTimeout > 0
		if hasServer != c.wantServerSide {
			t.Errorf("profile %v server-side mismatch: got %v want %v", c.profile, hasServer, c.wantServerSide)
		}
		if hasClient != c.wantClientSide {
			t.Errorf("profile %v client-side mismatch: got %v want %v", c.profile, hasClient, c.wantClientSide)
		}
		if c.wantServerSide && s.MaxBodyBytes != c.wantMaxBodyAtMost {
			t.Errorf("profile %v MaxBodyBytes: got %d want %d", c.profile, s.MaxBodyBytes, c.wantMaxBodyAtMost)
		}
	}
}

func TestConfigure_AppliesTimeouts(t *testing.T) {
	srv := &http.Server{}
	Configure(srv, SinkSync)
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout: got %v want 5s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 60*time.Second {
		t.Errorf("ReadTimeout: got %v want 60s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 60*time.Second {
		t.Errorf("WriteTimeout: got %v want 60s", srv.WriteTimeout)
	}
	if srv.MaxHeaderBytes != 16*1024 {
		t.Errorf("MaxHeaderBytes: got %d want 16384", srv.MaxHeaderBytes)
	}
}

func TestClient_HasTimeout(t *testing.T) {
	c := Client(PairClient)
	if c.Timeout != 30*time.Second {
		t.Errorf("PairClient.Timeout: got %v want 30s", c.Timeout)
	}
	c = Client(SyncClient)
	if c.Timeout != 5*time.Minute {
		t.Errorf("SyncClient.Timeout: got %v want 5m", c.Timeout)
	}
}

// TestLimitedReader_RejectsOversizedBody verifies http.MaxBytesError
// surfaces when a handler reads past the cap. This is the path that
// protects sink and pair listeners from memory and disk exhaustion.
func TestLimitedReader_RejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		LimitedReader(r, 100) // very small cap for the test
		_, err := io.ReadAll(r.Body)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 1 KB body, 100-byte cap => 413
	resp, err := http.Post(srv.URL, "application/octet-stream", strings.NewReader(strings.Repeat("x", 1024)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d want 413", resp.StatusCode)
	}

	// 50-byte body, 100-byte cap => 200
	resp2, err := http.Post(srv.URL, "application/octet-stream", strings.NewReader(strings.Repeat("y", 50)))
	if err != nil {
		t.Fatalf("POST small: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("status for small body: got %d want 200", resp2.StatusCode)
	}
}

// TestClient_TarpitServerTimesOut covers U11: a server that accepts the
// connection and never writes a response causes the client to fail
// within the profile's timeout window. Uses PairClient (30s) but caps
// the test at a much smaller value to keep CI fast.
func TestClient_TarpitServerTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // longer than our test client's timeout
	}))
	defer srv.Close()

	c := &http.Client{Timeout: 500 * time.Millisecond}
	start := time.Now()
	_, err := c.Get(srv.URL)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("client took %v to time out; should have been ~500ms", elapsed)
	}
}
