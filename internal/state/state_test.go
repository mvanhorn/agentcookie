package state

import (
	"os"
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
	for i := range 50 {
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

func TestSinkForKeysByPeerAndRefreshesURL(t *testing.T) {
	s := &SourceState{Role: "source"}
	a := s.SinkFor("alpha", "http://a.test/sync")
	a.TotalPushes = 3
	// Same peer, changed URL: same record, URL refreshed, history kept.
	again := s.SinkFor("alpha", "http://a-new.test/sync")
	if again.TotalPushes != 3 {
		t.Fatalf("expected history preserved across URL change, got %+v", again)
	}
	if again.URL != "http://a-new.test/sync" {
		t.Errorf("URL should refresh to current, got %q", again.URL)
	}
	if len(s.Sinks) != 1 {
		t.Fatalf("URL change must not orphan the record, got %d records", len(s.Sinks))
	}
	// A different peer is a distinct record.
	s.SinkFor("bravo", "http://b.test/sync")
	if len(s.Sinks) != 2 {
		t.Fatalf("distinct peer should add a record, got %d", len(s.Sinks))
	}
}

func TestLoadSourceLegacySinkURLDecodesWithoutError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source-state.json")
	// A pre-multi-sink state file: sink_url present, no sinks array.
	if err := os.WriteFile(path, []byte(`{"role":"source","sink_url":"http://legacy.test/sync","total_pushes":5}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	st, err := LoadSource(path)
	if err != nil {
		t.Fatalf("legacy state should decode without error: %v", err)
	}
	if st.SinkURL != "http://legacy.test/sync" || st.TotalPushes != 5 {
		t.Errorf("legacy fields lost: %+v", st)
	}
	if len(st.Sinks) != 0 {
		t.Errorf("legacy file has no per-sink records, got %d", len(st.Sinks))
	}
}
