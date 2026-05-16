package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePush is a counting Push function for tests.
type fakePush struct {
	count    atomic.Int64
	pushed   atomic.Int64
	mu       sync.Mutex
	pushChan chan struct{}
	failNext bool
}

func (f *fakePush) Push(ctx context.Context) (int, error) {
	f.count.Add(1)
	select {
	case f.pushChan <- struct{}{}:
	default:
	}
	if f.failNext {
		f.failNext = false
		return 0, fmt.Errorf("synthetic push failure")
	}
	f.pushed.Add(1)
	return 1, nil
}

func TestNewRequiresCookiesPath(t *testing.T) {
	_, err := New(Config{Push: func(context.Context) (int, error) { return 0, nil }})
	if err == nil {
		t.Error("expected error for missing CookiesPath")
	}
}

func TestNewRequiresPush(t *testing.T) {
	_, err := New(Config{CookiesPath: filepath.Join(t.TempDir(), "Cookies")})
	if err == nil {
		t.Error("expected error for missing Push callback")
	}
}

func TestNewRejectsBadParent(t *testing.T) {
	_, err := New(Config{
		CookiesPath: "/nonexistent-dir/Cookies",
		Push:        func(context.Context) (int, error) { return 0, nil },
	})
	if err == nil {
		t.Error("expected error when parent dir does not exist")
	}
}

func TestWatcherFiresAtStartup(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(cookies, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	fp := &fakePush{pushChan: make(chan struct{}, 4)}
	w, err := New(Config{
		CookiesPath:  cookies,
		Push:         fp.Push,
		Debounce:     50 * time.Millisecond,
		MinInterval:  100 * time.Millisecond,
		BaselineTick: time.Hour, // disable for this test
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go w.Run(ctx)

	select {
	case <-fp.pushChan:
		// got startup push
	case <-time.After(1 * time.Second):
		t.Fatal("expected startup push within 1s, got none")
	}
}

func TestWatcherFiresOnFileWrite(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(cookies, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	fp := &fakePush{pushChan: make(chan struct{}, 8)}
	w, err := New(Config{
		CookiesPath:  cookies,
		Push:         fp.Push,
		Debounce:     50 * time.Millisecond,
		MinInterval:  100 * time.Millisecond,
		BaselineTick: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)

	// Drain startup push.
	<-fp.pushChan

	// Wait past min-interval to ensure the next push fires.
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(cookies, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fp.pushChan:
	case <-time.After(2 * time.Second):
		t.Fatal("expected push after Cookies file write, got none")
	}
}

func TestWatcherDebouncesRapidWrites(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(cookies, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	fp := &fakePush{pushChan: make(chan struct{}, 64)}
	w, err := New(Config{
		CookiesPath:  cookies,
		Push:         fp.Push,
		Debounce:     200 * time.Millisecond,
		MinInterval:  10 * time.Millisecond,
		BaselineTick: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go w.Run(ctx)

	// Drain startup push.
	<-fp.pushChan
	time.Sleep(50 * time.Millisecond)
	start := fp.count.Load()

	// Hammer the file 10 times in 100ms.
	for i := 0; i < 10; i++ {
		_ = os.WriteFile(cookies, []byte{byte(i)}, 0o600)
		time.Sleep(10 * time.Millisecond)
	}
	// Wait through debounce.
	time.Sleep(500 * time.Millisecond)
	got := fp.count.Load() - start
	if got > 2 {
		t.Errorf("expected debounce to coalesce 10 writes into <=2 pushes, got %d", got)
	}
	if got == 0 {
		t.Error("expected at least one push after debounce window, got zero")
	}
}

func TestWatcherIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(cookies, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	fp := &fakePush{pushChan: make(chan struct{}, 4)}
	w, err := New(Config{
		CookiesPath:  cookies,
		Push:         fp.Push,
		Debounce:     50 * time.Millisecond,
		MinInterval:  100 * time.Millisecond,
		BaselineTick: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)

	// Drain startup push.
	<-fp.pushChan
	time.Sleep(150 * time.Millisecond)
	start := fp.count.Load()

	// Touch an unrelated file in the same dir.
	unrelated := filepath.Join(dir, "unrelated.txt")
	for i := 0; i < 5; i++ {
		_ = os.WriteFile(unrelated, []byte{byte(i)}, 0o600)
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	got := fp.count.Load() - start
	if got != 0 {
		t.Errorf("unrelated file touches should not trigger pushes, got %d", got)
	}
}

func TestIsInterestingCoversWALCompanions(t *testing.T) {
	w := &Watcher{cfg: Config{CookiesPath: "/tmp/Cookies"}}
	cases := map[string]bool{
		"/tmp/Cookies":         true,
		"/tmp/Cookies-wal":     true,
		"/tmp/Cookies-shm":     true,
		"/tmp/Cookies-journal": true,
		"/tmp/Other":           false,
		"/tmp/Cookies.bak":     false,
	}
	for path, want := range cases {
		got := w.isInteresting(path)
		if got != want {
			t.Errorf("isInteresting(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStatsAfterSuccessfulPush(t *testing.T) {
	dir := t.TempDir()
	cookies := filepath.Join(dir, "Cookies")
	if err := os.WriteFile(cookies, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	fp := &fakePush{pushChan: make(chan struct{}, 4)}
	w, err := New(Config{
		CookiesPath:  cookies,
		Push:         fp.Push,
		Debounce:     50 * time.Millisecond,
		MinInterval:  100 * time.Millisecond,
		BaselineTick: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go w.Run(ctx)
	<-fp.pushChan
	// Give runOne goroutine time to record stats.
	time.Sleep(100 * time.Millisecond)
	s := w.Stats()
	if s.PushCount == 0 {
		t.Errorf("expected PushCount > 0, got %d", s.PushCount)
	}
	if s.ErrorCount != 0 {
		t.Errorf("expected ErrorCount == 0, got %d", s.ErrorCount)
	}
}
