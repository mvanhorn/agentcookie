package destination

import (
	"context"
	"github.com/mvanhorn/agentcookie/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedRejectsCanonicalDefaultProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	actual := filepath.Join(home, "relocated-default")
	os.MkdirAll(actual, 0700)
	os.MkdirAll(filepath.Join(home, ".config"), 0700)
	if err := os.Symlink(actual, filepath.Join(home, ".config", "google-chrome")); err != nil {
		t.Fatal(err)
	}
	m := NewManaged(config.DestinationRef{Kind: "live_chrome", Chrome: config.DestinationChrome{UserDataDir: actual, Port: 19247, Path: "/bin/false"}})
	if err := m.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "default browser profile") {
		t.Fatalf("default alias reached launch: %v", err)
	}
}

func TestIdleOnDemandDoesNotProbeUntilSnapshot(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "not a browser", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := NewManaged(config.DestinationRef{Kind: "attach", Endpoint: srv.URL, Chrome: config.DestinationChrome{Start: "on_demand"}})
	defer m.Close()
	e, err := Open(t.TempDir(), nil, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_ = m.Run(ctx, e)
	if calls.Load() != 0 {
		t.Fatal("idle destination probed or launched browser")
	}
	st, err := e.Accept(context.Background(), "source", snap(1, item("token", "one")))
	if err == nil || st.Accepted != 1 || calls.Load() == 0 {
		t.Fatalf("snapshot did not activate browser: %+v %v", st, err)
	}
}
