package state

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWriterSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source-state.json")
	w := NewWriter(path)

	want := &SourceState{
		Role:          "source",
		LastPush:      time.Now().UTC().Truncate(time.Second),
		LastPushCount: 12,
		TotalPushes:   42,
		SinkURL:       "http://test:9999/sync",
	}
	if err := w.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadSource(path)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if got == nil {
		t.Fatal("LoadSource returned nil")
	}
	if got.Role != want.Role || got.LastPushCount != want.LastPushCount || got.TotalPushes != want.TotalPushes {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestLoadSourceMissingFile(t *testing.T) {
	got, err := LoadSource(filepath.Join(t.TempDir(), "no-such.json"))
	if err != nil {
		t.Errorf("LoadSource on missing file should not error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing file, got %+v", got)
	}
}

func TestLoadSinkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sink-state.json")
	w := NewWriter(path)

	want := &SinkState{
		Role:           "sink",
		LastWrite:      time.Now().UTC().Truncate(time.Second),
		LastWriteCount: 7,
		LastWriteMode:  "cdp-managed",
		TotalWrites:    99,
		ListenAddr:     "100.x.y.z:9999",
		CDPManaged:     true,
	}
	if err := w.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadSink(path)
	if err != nil {
		t.Fatalf("LoadSink: %v", err)
	}
	if got == nil || got.Role != "sink" || got.LastWriteMode != "cdp-managed" || got.TotalWrites != 99 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestWriterIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source-state.json")
	w := NewWriter(path)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := &SourceState{Role: "source", TotalPushes: i}
			_ = w.Save(s)
		}(i)
	}
	wg.Wait()

	got, err := LoadSource(path)
	if err != nil {
		t.Fatalf("LoadSource after concurrent saves: %v", err)
	}
	if got == nil {
		t.Fatal("LoadSource returned nil after writes")
	}
	// Any save's value is acceptable; just confirm the file is valid JSON.
}
