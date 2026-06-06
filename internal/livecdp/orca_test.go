package livecdp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestFindOrcaWebviewTargets(t *testing.T) {
	// /json/list with a mix of orca's own UI (page) and two browser panes
	// (webview). Only the webviews are injectable browser panes.
	body := `[
		{"id":"ui1","type":"page","url":"file:///Applications/Orca.app/.../renderer"},
		{"id":"pane1","type":"webview","url":"https://github.com/"},
		{"id":"pane2","type":"webview","url":"https://example.com/"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	ids, err := FindOrcaWebviewTargets(srv.URL)
	if err != nil {
		t.Fatalf("FindOrcaWebviewTargets: %v", err)
	}
	if len(ids) != 2 || ids[0] != "pane1" || ids[1] != "pane2" {
		t.Fatalf("expected [pane1 pane2], got %v", ids)
	}
}

func TestFindOrcaWebviewTargetsNoPanes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"ui1","type":"page","url":"file:///renderer"}]`))
	}))
	defer srv.Close()

	ids, err := FindOrcaWebviewTargets(srv.URL)
	if err != nil {
		t.Fatalf("FindOrcaWebviewTargets: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected no webview targets, got %v", ids)
	}
}

func TestFindOrcaWebviewTargetsUnreachable(t *testing.T) {
	// A closed endpoint should surface an actionable error, not panic.
	_, err := FindOrcaWebviewTargets("http://127.0.0.1:0")
	if err == nil {
		t.Fatal("expected an error connecting to a closed CDP endpoint")
	}
}

func TestInjectIntoOrcaEmptyCookies(t *testing.T) {
	// No cookies is a no-op and must not even touch the endpoint.
	n, err := InjectIntoOrca("http://127.0.0.1:0", nil)
	if err != nil || n != 0 {
		t.Fatalf("expected (0, nil) for empty cookies, got (%d, %v)", n, err)
	}
}

func TestInjectIntoOrcaNoPanes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"ui1","type":"page","url":"file:///renderer"}]`))
	}))
	defer srv.Close()

	// Cookies present but no open pane: returns (0, nil), a soft outcome.
	n, err := InjectIntoOrca(srv.URL, []chrome.Cookie{{HostKey: "github.com", Name: "a", Value: "1", Path: "/"}})
	if err != nil || n != 0 {
		t.Fatalf("expected (0, nil) when no panes are open, got (%d, %v)", n, err)
	}
}
