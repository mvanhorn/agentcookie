package reader

import (
	"context"
	"database/sql"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/gecko"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/source"
	"path/filepath"
	"testing"
)

func TestFileAdapterHoldsAmbiguousSites(t *testing.T) {
	p := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA user_version=17;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT);INSERT INTO moz_cookies VALUES('example.com','a','one','/',2000000000000,1,0,1,''),('example.com','b','two','/',2000000000000,1,0,1,'^userContextId=6')`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	r, e := Open(context.Background(), Options{Name: "firefox", Profile: p, Mode: "file", StateDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	s, e := r.Read(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Cookies) != 0 || s.Meta.Sites["example.com"].State != protocol.Held || s.Meta.Surfaces[protocol.Cookies].State != protocol.Incomplete || s.Meta.Surfaces[protocol.IndexedDB].State != protocol.Unsupported {
		t.Fatalf("%+v", s)
	}
	r.opts.Containers = []string{"6"}
	s, e = r.Read(context.Background(), nil)
	if e != nil || len(s.Cookies) != 1 || s.Cookies[0].Name != "b" {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestRerouteUsesOneCapture(t *testing.T) {
	snap := &source.Snapshot{Meta: protocol.SnapshotMeta{Generation: 7}, RoutingData: &Capture{Records: []gecko.Record{{Site: "example.com", Container: 1, Cookie: chrome.Cookie{HostKey: "example.com", Name: "sid", Value: "personal"}}, {Site: "example.com", Container: 6, Cookie: chrome.Cookie{HostKey: "example.com", Name: "sid", Value: "work"}}}}}
	a, e := Reroute(snap, nil, []string{"1"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Reroute(snap, nil, []string{"6"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Cookies) != 1 || len(b.Cookies) != 1 || a.Cookies[0].Value != "personal" || b.Cookies[0].Value != "work" || a.Meta.Generation != 7 || b.Meta.Generation != 7 || len(snap.Cookies) != 0 {
		t.Fatal(a, b, snap)
	}
}
func TestRerouteDoesNotAliasMetadata(t *testing.T) {
	snap := &source.Snapshot{Meta: protocol.SnapshotMeta{Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}}}, RoutingData: &Capture{Records: []gecko.Record{{Site: "example.com", Container: 0, Cookie: chrome.Cookie{HostKey: "example.com"}}}}}
	a, e := Reroute(snap, nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Reroute(snap, nil, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	a.Meta.Surfaces[protocol.Cookies] = protocol.SurfaceStatus{State: protocol.Incomplete}
	a.Meta.Sites["example.com"] = protocol.SiteStatus{State: protocol.Held}
	if b.Meta.Surfaces[protocol.Cookies].State != protocol.Complete || b.Meta.Sites["example.com"].State != protocol.Shipped || snap.Meta.Surfaces[protocol.Cookies].State != protocol.Complete {
		t.Fatal("routing aliases source metadata")
	}
}
