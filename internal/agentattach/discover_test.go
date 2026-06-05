package agentattach

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// versionHandler serves a /json/version payload.
func versionHandler(browser, ws string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Browser":"` + browser + `","webSocketDebuggerUrl":"` + ws + `"}`))
	})
	return mux
}

func TestDiscoverAt_ReachableAutoConnect(t *testing.T) {
	srv := httptest.NewServer(versionHandler("Chrome/148.0.7778.217", "ws://127.0.0.1:9222/devtools/browser/abc"))
	defer srv.Close()

	d := discoverAt(context.Background(), srv.URL, 9222, func() int { return 0 })

	if !d.Reachable {
		t.Fatal("expected Reachable")
	}
	if d.WSEndpoint != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Errorf("WSEndpoint = %q", d.WSEndpoint)
	}
	if d.Version != 148 {
		t.Errorf("Version = %d, want 148", d.Version)
	}
	if d.Tier != TierAutoConnect {
		t.Errorf("Tier = %v, want auto-connect", d.Tier)
	}
	if d.Remediation != "" {
		t.Errorf("Remediation = %q, want empty for a usable endpoint", d.Remediation)
	}
}

func TestDiscoverAt_ReachableNoWebSocket(t *testing.T) {
	srv := httptest.NewServer(versionHandler("Chrome/148.0.7778.217", ""))
	defer srv.Close()

	d := discoverAt(context.Background(), srv.URL, 9222, func() int { return 0 })

	if !d.Reachable {
		t.Fatal("expected Reachable")
	}
	if d.Remediation == "" || !strings.Contains(d.Remediation, "WebSocket") {
		t.Errorf("expected a no-WebSocket remediation, got %q", d.Remediation)
	}
}

func TestDiscoverAt_UnreachableAutoConnect(t *testing.T) {
	// Closed server -> connection refused -> unreachable path.
	srv := httptest.NewServer(versionHandler("", ""))
	url := srv.URL
	srv.Close()

	d := discoverAt(context.Background(), url, 9222, func() int { return 148 })

	if d.Reachable {
		t.Fatal("expected unreachable")
	}
	if d.Version != 148 || d.Tier != TierAutoConnect {
		t.Errorf("Version/Tier = %d/%v, want 148/auto-connect", d.Version, d.Tier)
	}
	if !strings.Contains(d.Remediation, "chrome://inspect") {
		t.Errorf("expected chrome://inspect enable step, got %q", d.Remediation)
	}
}

func TestDiscoverAt_UnreachableCustomDirOnly(t *testing.T) {
	srv := httptest.NewServer(versionHandler("", ""))
	url := srv.URL
	srv.Close()

	d := discoverAt(context.Background(), url, 9222, func() int { return 140 })

	if d.Tier != TierCustomDirOnly {
		t.Errorf("Tier = %v, want custom-dir-only", d.Tier)
	}
	if !strings.Contains(d.Remediation, "--fallback") {
		t.Errorf("expected --fallback remediation, got %q", d.Remediation)
	}
}

func TestDiscoverAt_UnreachableUnknownVersion(t *testing.T) {
	srv := httptest.NewServer(versionHandler("", ""))
	url := srv.URL
	srv.Close()

	d := discoverAt(context.Background(), url, 9222, func() int { return 0 })

	if d.Tier != TierUnknown {
		t.Errorf("Tier = %v, want unknown", d.Tier)
	}
	if !strings.Contains(d.Remediation, "--fallback") {
		t.Errorf("expected --fallback remediation, got %q", d.Remediation)
	}
}

func TestDiscoverAt_Non200IsUnreachable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := discoverAt(context.Background(), srv.URL, 9222, func() int { return 148 })

	if d.Reachable {
		t.Fatal("a non-200 /json/version must be treated as unreachable")
	}
	if d.Tier != TierAutoConnect {
		t.Errorf("Tier = %v, want auto-connect (from installed version)", d.Tier)
	}
}

func TestDiscoverAt_UnreachableLegacy(t *testing.T) {
	srv := httptest.NewServer(versionHandler("", ""))
	url := srv.URL
	srv.Close()

	d := discoverAt(context.Background(), url, 9222, func() int { return 120 })

	if d.Tier != TierLegacy {
		t.Errorf("Tier = %v, want legacy", d.Tier)
	}
	if !strings.Contains(d.Remediation, "--remote-debugging-port") {
		t.Errorf("legacy remediation should mention --remote-debugging-port, got %q", d.Remediation)
	}
}
