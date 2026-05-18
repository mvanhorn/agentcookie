package protocol

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestPersistentTracker_WriteThroughOnEachAccept exercises the happy
// path: every accepted envelope causes the store to see the new
// high-water mark, so a subsequent process can read it back.
func TestPersistentTracker_WriteThroughOnEachAccept(t *testing.T) {
	store := NewMemorySequenceStore(nil)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	if !tr.Accept("laptop", 1000) {
		t.Fatal("first sequence must be accepted")
	}
	if got := store.State["laptop"]; got != 1000 {
		t.Errorf("store did not see 1000 after first accept; got %d", got)
	}
	if !tr.Accept("laptop", 2000) {
		t.Fatal("higher sequence must be accepted")
	}
	if got := store.State["laptop"]; got != 2000 {
		t.Errorf("store did not see 2000 after second accept; got %d", got)
	}
	if store.SaveCount != 2 {
		t.Errorf("expected 2 saves, got %d", store.SaveCount)
	}
}

// TestPersistentTracker_RejectsReplayAfterRestart simulates a sink
// restart: build a fresh tracker from the same store and confirm a
// captured payload at or below the prior high-water mark is rejected.
// This is the regression that closes S9: in v0.11, a restart cleared
// the in-memory map and reopened the replay window.
func TestPersistentTracker_RejectsReplayAfterRestart(t *testing.T) {
	store := NewMemorySequenceStore(nil)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	if !tr.Accept("laptop", 5000) {
		t.Fatal("first sequence must be accepted")
	}
	// Simulate restart: same backing store, new tracker.
	tr2, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore (restart): %v", err)
	}
	if tr2.Accept("laptop", 5000) {
		t.Fatal("replay of last-seen sequence must be rejected after restart")
	}
	if tr2.Accept("laptop", 4999) {
		t.Fatal("lower sequence must be rejected after restart")
	}
	if !tr2.Accept("laptop", 5001) {
		t.Fatal("strictly higher sequence must still be accepted after restart")
	}
}

// TestPersistentTracker_NanosecondGranularity asserts that two
// envelopes generated within a microsecond of each other can both be
// accepted, which is the source-side regression v0.12 closes by
// switching time.Now().Unix() to time.Now().UnixNano().
func TestPersistentTracker_NanosecondGranularity(t *testing.T) {
	store := NewMemorySequenceStore(nil)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	// Two sequences within 1 microsecond of each other. With Unix()
	// seconds these would collide (same value, second rejected as
	// replay). With UnixNano() they are distinct positive int64s.
	seq1 := int64(1_700_000_000_000_000_000)
	seq2 := seq1 + 250 // 250 nanoseconds later
	if !tr.Accept("laptop", seq1) {
		t.Fatal("first nano sequence must be accepted")
	}
	if !tr.Accept("laptop", seq2) {
		t.Fatal("250ns-later sequence must be accepted with nano granularity")
	}
}

// TestNewTrackerFromStore_CorruptFile asserts that a corrupt
// sequence.json fails the tracker constructor so the sink boot path
// can surface a clear error to the operator. Silently falling through
// to an empty tracker would re-open the replay window.
func TestNewTrackerFromStore_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")
	if err := os.WriteFile(path, []byte("not json {"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	store := NewFileSequenceStore(path)
	tr, err := NewTrackerFromStore(store)
	if err == nil {
		t.Fatalf("expected error from corrupt sequence file, got tracker %+v", tr)
	}
	// Surface the recovery instruction in the error message so an
	// operator sees what to do.
	if msg := err.Error(); msg == "" {
		t.Errorf("expected non-empty error message, got %q", msg)
	}
}

// TestNewTrackerFromStore_MissingFile asserts that a missing
// sequence.json on first-ever boot initializes an empty tracker
// without error.
func TestNewTrackerFromStore_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")
	store := NewFileSequenceStore(path)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if tr.Last("laptop") != 0 {
		t.Errorf("expected empty tracker, got Last=%d", tr.Last("laptop"))
	}
	if !tr.Accept("laptop", 100) {
		t.Fatal("first accept on empty tracker must succeed")
	}
}

// TestPersistentTracker_FileBackingRoundTrip wires the real
// fileSequenceStore (under t.TempDir()) and exercises the full
// load -> accept -> save -> reload cycle.
func TestPersistentTracker_FileBackingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.json")
	store := NewFileSequenceStore(path)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	if !tr.Accept("a", 100) || !tr.Accept("b", 200) || !tr.Accept("a", 150) {
		t.Fatal("unexpected reject during seed")
	}
	// File should exist now with mode 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sequence file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected mode 0600, got %o", info.Mode().Perm())
	}
	// Restart cycle.
	tr2, err := NewTrackerFromStore(NewFileSequenceStore(path))
	if err != nil {
		t.Fatalf("NewTrackerFromStore (reload): %v", err)
	}
	if tr2.Last("a") != 150 || tr2.Last("b") != 200 {
		t.Errorf("reload lost state: a=%d b=%d", tr2.Last("a"), tr2.Last("b"))
	}
	if tr2.Accept("a", 150) {
		t.Error("replay at last high-water mark must be rejected after reload")
	}
	if tr2.Accept("b", 100) {
		t.Error("lower sequence must be rejected after reload")
	}
}

// TestPersistentTracker_RollsBackOnSaveFailure asserts that a Save
// failure does not leave the in-memory map ahead of disk. If the
// save fails, the in-memory high-water mark stays at the prior value
// so the sink does not later reject a legitimate retry.
func TestPersistentTracker_RollsBackOnSaveFailure(t *testing.T) {
	store := NewMemorySequenceStore(map[string]int64{"laptop": 100})
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	store.FailSave = errors.New("simulated disk failure")
	if tr.Accept("laptop", 200) {
		t.Fatal("Accept must return false when Save fails")
	}
	// In-memory state must be rolled back to the prior high-water.
	if got := tr.Last("laptop"); got != 100 {
		t.Errorf("expected rollback to 100, got %d", got)
	}
	// Recover: subsequent successful save should accept 200.
	store.FailSave = nil
	if !tr.Accept("laptop", 200) {
		t.Fatal("Accept must succeed once Save recovers")
	}
}

// TestPersistentTracker_LegacySecondGranularityStillWorks is the
// regression scenario for the plan's "legacy sources sending
// 1-second sequences" requirement.
func TestPersistentTracker_LegacySecondGranularityStillWorks(t *testing.T) {
	store := NewMemorySequenceStore(nil)
	tr, err := NewTrackerFromStore(store)
	if err != nil {
		t.Fatalf("NewTrackerFromStore: %v", err)
	}
	// Two legacy syncs one second apart.
	if !tr.Accept("legacy", 1_700_000_000) {
		t.Fatal("first legacy sequence must be accepted")
	}
	if !tr.Accept("legacy", 1_700_000_001) {
		t.Fatal("monotonically increasing legacy sequence must be accepted")
	}
	// Replay rejected as before.
	if tr.Accept("legacy", 1_700_000_001) {
		t.Error("legacy replay must be rejected")
	}
}
