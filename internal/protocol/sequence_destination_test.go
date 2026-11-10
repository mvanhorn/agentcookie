package protocol

import (
	"errors"
	"testing"
)

func TestDestinationReplayMigration(t *testing.T) {
	s := NewMemorySequenceStore(map[string]int64{"source": 100})
	tr, _ := NewTrackerFromStore(s)
	if tr.AcceptDestination("source", "a", 100) {
		t.Fatal("captured legacy accepted")
	}
	if !tr.AcceptDestination("source", "a", 200) || !tr.AcceptDestination("source", "b", 150) {
		t.Fatal("out of order destinations rejected")
	}
	if tr.AcceptDestination("source", "a", 200) || tr.AcceptDestination("source", "b", 150) {
		t.Fatal("replay accepted")
	}
	if s.State["source"] != 200 {
		t.Fatal("legacy high water regressed")
	}
	// Old binary rollback still reads map[string]int64 and advances legacy.
	old, _ := NewTrackerFromStore(s)
	if !old.Accept("source", 250) {
		t.Fatal("v2 refused")
	}
	tr, _ = NewTrackerFromStore(s)
	if tr.AcceptDestination("source", "a", 240) || tr.AcceptDestination("source", "new", 240) {
		t.Fatal("rollback floor ignored")
	}
	if !tr.AcceptDestination("source", "b", 300) {
		t.Fatal("re-upgrade refused")
	}
	if tr.AcceptDestination("source", "../escape", 999) {
		t.Fatal("invalid namespace")
	}
}
func TestDestinationReplayAtomicFailure(t *testing.T) {
	s := NewMemorySequenceStore(nil)
	tr, _ := NewTrackerFromStore(s)
	s.FailSave = errors.New("disk full")
	if tr.AcceptDestination("source", "a", 10) {
		t.Fatal("non-durable accept")
	}
	s.FailSave = nil
	if !tr.AcceptDestination("source", "a", 10) {
		t.Fatal("not rolled back")
	}
}

func TestDestinationNamespacesSurviveRestartAndUnsafeLabels(t *testing.T) {
	store := NewMemorySequenceStore(map[string]int64{"source": 100})
	tr, _ := NewTrackerFromStore(store)
	if !tr.AcceptDestination("source", "a", 200) {
		t.Fatal("first destination")
	}
	tr, _ = NewTrackerFromStore(store)
	if !tr.AcceptDestination("source", "b", 150) {
		t.Fatal("restart lost migration floor")
	}
	for _, label := range []string{"source/@floor", "source/other", "../source", "source\\bad"} {
		if tr.Accept(label, 999) || tr.AcceptDestination(label, "a", 999) {
			t.Fatalf("unsafe source accepted: %q", label)
		}
	}
	if tr.AcceptDestination("source", "@floor", 999) {
		t.Fatal("reserved destination")
	}
}
