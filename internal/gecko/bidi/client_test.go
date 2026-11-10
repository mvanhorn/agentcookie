package bidi

import (
	"context"
	"encoding/json"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/mvanhorn/agentcookie/internal/gecko/socket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestForeignSessionNeverEnded(t *testing.T) {
	methods := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, _, e := ws.UpgradeHTTP(r, w)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			b, e := socket.ReadBounded(c, ws.StateServerSide)
			if e != nil {
				return
			}
			var m socket.Message
			json.Unmarshal(b, &m)
			methods <- m.Method
			reply, _ := json.Marshal(map[string]any{"id": m.ID, "type": "error", "error": "session not created", "message": "already exists"})
			wsutil.WriteServerText(c, reply)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, e := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/session")
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if e = client.NewSession(ctx, t.TempDir()); e == nil {
		t.Fatal("foreign session accepted")
	}
	if e = client.End(ctx); e != nil {
		t.Fatal(e)
	}
	if m := <-methods; m != "session.new" {
		t.Fatal(m)
	}
	select {
	case m := <-methods:
		t.Fatalf("foreign session modified: %s", m)
	default:
	}
}
func TestProfileMismatchEndsOnlyCreatedSession(t *testing.T) {
	methods := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _, _, e := ws.UpgradeHTTP(r, w)
		if e != nil {
			return
		}
		defer c.Close()
		for {
			b, e := socket.ReadBounded(c, ws.StateServerSide)
			if e != nil {
				return
			}
			var m socket.Message
			json.Unmarshal(b, &m)
			methods <- m.Method
			result := map[string]any{}
			if m.Method == "session.new" {
				result = map[string]any{"sessionId": "own", "capabilities": map[string]string{"moz:profile": "/nonexistent-fixture"}}
			}
			reply, _ := json.Marshal(map[string]any{"id": m.ID, "result": result})
			wsutil.WriteServerText(c, reply)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, e := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/session")
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if e = client.NewSession(ctx, t.TempDir()); e == nil {
		t.Fatal("profile mismatch accepted")
	}
	if <-methods != "session.new" || <-methods != "session.end" {
		t.Fatal("wrong methods")
	}
}
