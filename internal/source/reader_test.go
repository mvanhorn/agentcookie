package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
)

func TestCDPDoesNotInvokeFileOrCredentialProviders(t *testing.T) {
	forbidden := func() { t.Fatal("CDP touched file provider") }
	r, err := New(&config.SourceConfig{CDPSource: config.CDPSourceRef{Enabled: true, Endpoint: "http://127.0.0.1:9001"}}, Options{}, Dependencies{Password: func(chrome.Browser) (string, error) { forbidden(); return "", nil }, ReadCookies: func(string, string, []byte) ([]chrome.Cookie, error) { forbidden(); return nil, nil }, Pack: func(string, int64) ([]byte, []string, error) { forbidden(); return nil, nil, nil }, ReadCDP: func(context.Context, string) ([]chrome.Cookie, error) { return nil, errors.New("fixture offline") }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.Read(context.Background(), &config.Blocklist{Version: 1}); err == nil {
		t.Fatal("CDP failure hidden")
	}
	if !r.Capabilities().Live || len(r.Capabilities().Surfaces) != 1 || r.WatchPaths().CookiesPath != "" {
		t.Fatalf("unexpected CDP capabilities: %#v", r.Capabilities())
	}
}
func TestChromiumSnapshotKeepsLegacyLanes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Profile 3", "Network", "Cookies")
	cfg := &config.SourceConfig{Chrome: config.ChromeRef{DBPath: path}, Browser: config.BrowserRef{Name: "chrome"}}
	var paths []string
	r, err := NewChromiumFileWithKey(cfg, Options{LocalStorage: true, IndexedDB: true}, Dependencies{ReadCookies: func(p, pattern string, key []byte) ([]chrome.Cookie, error) {
		if p != path || pattern != "%" {
			t.Fatal("wrong profile")
		}
		return []chrome.Cookie{{HostKey: "example.com", Name: "auth", Value: "fixture"}}, nil
	}, Pack: func(p string, cap int64) ([]byte, []string, error) {
		paths = append(paths, p)
		return []byte("fixture"), []string{"z", "a"}, nil
	}}, []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snap, err := r.Read(context.Background(), &config.Blocklist{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Cookies) != 1 || len(snap.LocalStorageTarball) == 0 || len(snap.IndexedDBTarball) == 0 {
		t.Fatalf("lost source lanes: %#v", snap)
	}
	expected := []string{filepath.Join(filepath.Dir(filepath.Dir(path)), "Local Storage", "leveldb"), filepath.Join(filepath.Dir(filepath.Dir(path)), "IndexedDB")}
	if !reflect.DeepEqual(paths, expected) {
		t.Fatalf("storage profile mismatch: %v", paths)
	}
	if !reflect.DeepEqual(snap.IndexedDBSkipped, []string{"a", "z"}) || snap.Meta.Surfaces[protocol.IndexedDB].State != protocol.Incomplete {
		t.Fatal("cap completeness or stable sorting lost")
	}
}
func TestCountersConcurrentDurableAndRollbackClock(t *testing.T) {
	dir := t.TempDir()
	clock := func() time.Time { return time.Unix(0, 1000) }
	c := Counters{Dir: dir, Now: clock}
	const n = 40
	values := make(chan int64, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			v, err := c.NextSequence()
			if err != nil {
				errs <- err
				return
			}
			values <- v
		})
	}
	wg.Wait()
	close(values)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for v := range values {
		if seen[v] {
			t.Fatal("duplicate sequence")
		}
		seen[v] = true
	}
	restarted := Counters{Dir: dir, Now: func() time.Time { return time.Unix(0, 1) }}
	v, err := restarted.NextSequence()
	if err != nil || v != 1040 {
		t.Fatalf("rollback/restart sequence %d: %v", v, err)
	}
	path := filepath.Join(dir, "source-state.json")
	if err := state.NewWriter(path).Save(&state.SourceState{LastSequence: 2, Role: "source"}); err != nil {
		t.Fatal(err)
	}
	v, err = restarted.NextSequence()
	if err != nil || v != 1041 {
		t.Fatalf("status write lowered sequence: %d %v", v, err)
	}
	for expected := uint64(1); expected < 4; expected++ {
		g, err := c.NextGeneration()
		if err != nil || g != expected {
			t.Fatalf("generation %d %v", g, err)
		}
	}
	for _, name := range []string{"source-state.json", "source-generation.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("state not private %s: %v", name, err)
		}
	}
}
func TestCountersCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source-generation.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Counters{Dir: dir}).NextGeneration(); err == nil {
		t.Fatal("corrupt generation reset")
	}
	if err := os.WriteFile(filepath.Join(dir, "source-state.json"), []byte(`{"last_sequence":9223372036854775807}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Counters{Dir: dir}).NextSequence(); err == nil {
		t.Fatal("sequence overflow")
	}
}

func TestReseedGenerationAfterStateLoss(t *testing.T) {
	c := Counters{Dir: t.TempDir()}
	g, err := c.ReseedGeneration(71)
	if err != nil || g != 71 {
		t.Fatalf("generation reseed = %d %v", g, err)
	}
	g, err = c.ReseedGeneration(10)
	if err != nil || g != 72 {
		t.Fatalf("generation lowered = %d %v", g, err)
	}
	g, err = c.NextGeneration()
	if err != nil || g != 73 {
		t.Fatalf("generation restart = %d %v", g, err)
	}
}
