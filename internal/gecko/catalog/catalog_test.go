package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoveryDoesNotReadStores(t *testing.T) {
	p := t.TempDir()
	d := filepath.Join(p, "storage", "default")
	os.MkdirAll(d, 0700)
	for _, n := range []string{"http+++localhost+18765", "http+++127.0.0.1+18766^userContextId=6", "https+++example.com^future=1"} {
		os.Mkdir(filepath.Join(d, n), 0700)
		os.WriteFile(filepath.Join(d, n, "cookies.sqlite"), []byte("not a database"), 0000)
	}
	c := New()
	if e := c.Discover(p, time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(c.Entries) != 3 {
		t.Fatal(c.Entries)
	}
	if c.Unread() != 3 {
		t.Fatal(c.Unread())
	}
	e := c.Entries[Key("http://localhost:18765", 0, "")]
	if e == nil {
		t.Fatal(c.Entries)
	}
	c.ReadResult(e.Key(), 1, true, nil, time.Now())
	os.RemoveAll(filepath.Join(d, "http+++localhost+18765"))
	c.Discover(p, time.Now())
	if e.Status == "removed" {
		t.Fatal("listing deleted entry")
	}
	c.ReadResult(e.Key(), 2, true, nil, time.Now())
	if e.Status != "removed" {
		t.Fatal(e.Status)
	}
}
func TestParse(t *testing.T) {
	for _, n := range []string{"http+++localhost+18765", "https+++example.com", "http+++[++1]+8080"} {
		o, _, _, _, e := ParseDirectory(n)
		if e != nil {
			t.Fatalf("%s %v", n, e)
		}
		if o == "" {
			t.Fatal(n)
		}
	}
}
func TestUnknownAttributePreservesContainer(t *testing.T) {
	_, n, _, reason, e := ParseDirectory("https+++example.com^futureIsolation=value&userContextId=6")
	if e != nil || n != 6 || reason == "" {
		t.Fatalf("container=%d reason=%q err=%v", n, reason, e)
	}
}
