package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/mvanhorn/agentcookie/internal/gecko/bidi"
	"github.com/mvanhorn/agentcookie/internal/gecko/socket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInterceptInstallsContextGuardBeforeDocument(t *testing.T) {
	calls := make(chan socket.Message, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, e := ws.UpgradeHTTP(r, w)
		if e != nil {
			return
		}
		defer conn.Close()
		n := 0
		for {
			b, e := socket.ReadBounded(conn, ws.StateServerSide)
			if e != nil {
				return
			}
			var m socket.Message
			json.Unmarshal(b, &m)
			calls <- m
			n++
			out := map[string]any{}
			if m.Method == "network.addIntercept" {
				out["intercept"] = fmt.Sprintf("i%d", n)
			}
			reply, _ := json.Marshal(map[string]any{"id": m.ID, "result": out})
			if e = wsutil.WriteServerText(conn, reply); e != nil {
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b, e := bidi.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/session")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	table := newInterceptTable(b)
	nonce := "abcdefghijklmnop"
	if e = table.add(ctx, "https://example.com", nonce); e != nil {
		t.Fatal(e)
	}
	if m := <-calls; m.Method != "network.addIntercept" {
		t.Fatal(m)
	}
	params, _ := json.Marshal(map[string]any{"context": "helper", "isBlocked": true, "request": map[string]string{"request": "document", "url": "https://example.com/.agentcookie-read/" + nonce}})
	table.handle(socket.Message{Method: "network.beforeRequestSent", Params: params})
	if m := <-calls; m.Method != "network.addIntercept" || !strings.Contains(string(m.Params), "helper") {
		t.Fatal(m)
	}
	if m := <-calls; m.Method != "network.provideResponse" || !strings.Contains(string(m.Params), "Content-Security-Policy") {
		t.Fatal(m)
	}
	params, _ = json.Marshal(map[string]any{"context": "helper", "isBlocked": true, "request": map[string]string{"request": "subresource", "url": "https://example.com/leak"}})
	table.handle(socket.Message{Method: "network.beforeRequestSent", Params: params})
	if m := <-calls; m.Method != "network.failRequest" {
		t.Fatal(m)
	}
	if e = table.remove(ctx, nonce); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if m := <-calls; m.Method != "network.removeIntercept" {
			t.Fatal(m)
		}
	}
}
func TestInterceptRejectsUnsafeOriginAndNonce(t *testing.T) {
	table := newInterceptTable(nil)
	for _, origin := range []string{"file:///tmp/fixture", "https://example.com/path", "https://user@example.com", "https://example.com?secret=x"} {
		if e := table.add(context.Background(), origin, "abcdefghijklmnop"); e == nil {
			t.Fatal(origin)
		}
	}
	if e := table.add(context.Background(), "https://example.com", "</title>"); e == nil {
		t.Fatal("unsafe nonce")
	}
}
