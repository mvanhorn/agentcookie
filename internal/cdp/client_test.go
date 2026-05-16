package cdp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// TestProbeRecognizesDebuggerURL spins up a fake /json/version endpoint and
// confirms Probe returns the embedded webSocketDebuggerUrl.
func TestProbeRecognizesDebuggerURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"Chrome/test","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`))
	}))
	defer srv.Close()

	host, port := splitHostPort(t, srv.Listener.Addr().String())
	info, err := Probe(context.Background(), host, port)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !strings.HasPrefix(info.WebSocketDebuggerURL, "ws://") {
		t.Errorf("expected ws:// URL, got %q", info.WebSocketDebuggerURL)
	}
	if info.Browser != "Chrome/test" {
		t.Errorf("Browser field wrong: %q", info.Browser)
	}
}

// TestProbeFailsWhenChromeDown verifies Probe returns an error when no
// listener is reachable.
func TestProbeFailsWhenChromeDown(t *testing.T) {
	// Pick a free port, immediately close so it's unreachable.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port := splitHostPort(t, ln.Addr().String())
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := Probe(ctx, host, port); err == nil {
		t.Fatal("expected error probing closed port, got nil")
	}
}

// TestCallEndToEnd uses a fake CDP server that echoes Storage.setCookies as
// success. Exercises the request encoding, response demux, and result decode.
func TestCallEndToEnd(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(msg, &req); err != nil {
				continue
			}
			// Echo a success response.
			resp, _ := json.Marshal(map[string]interface{}{
				"id":     req.ID,
				"result": map[string]interface{}{"ok": true, "method": req.Method},
			})
			_ = ws.WriteMessage(websocket.TextMessage, resp)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var result struct {
		OK     bool   `json:"ok"`
		Method string `json:"method"`
	}
	if err := conn.Call(ctx, "Storage.setCookies", map[string]interface{}{"cookies": []chrome.Cookie{}}, &result); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !result.OK {
		t.Error("expected ok=true in result")
	}
	if result.Method != "Storage.setCookies" {
		t.Errorf("method round-trip wrong: %q", result.Method)
	}
}

// TestSetCookiesEmptyIsNoOp guards against firing a CDP call when nothing is
// to be done.
func TestSetCookiesEmptyIsNoOp(t *testing.T) {
	// No connection needed; SetCookies returns early.
	n, err := SetCookies(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("expected no error for empty cookies, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 written for empty input, got %d", n)
	}
}

// TestSameSiteMapping pins the int -> CDP enum mapping for SameSite. Changes
// here would silently break cookies that rely on Strict / Lax semantics.
func TestSameSiteMapping(t *testing.T) {
	cases := map[int]string{
		0:  "None",
		1:  "Lax",
		2:  "Strict",
		-1: "", // Unspecified - omit field
		99: "", // unknown - omit field
	}
	for in, want := range cases {
		got := sameSiteString(in)
		if got != want {
			t.Errorf("sameSiteString(%d) = %q, want %q", in, got, want)
		}
	}
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort %q: %v", addr, err)
	}
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}
	return host, port
}
