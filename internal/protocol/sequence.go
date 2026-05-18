package protocol

import (
	"fmt"
	"sync"
)

// SequenceTracker is the sink's record of the highest Sequence number
// seen per source. Rejects equal-or-lower numbers to defend against
// replay of captured payloads. State is now persisted via SequenceStore
// (typically ~/.agentcookie/sequence.json) so a sink restart does not
// re-open a replay window. v0.12 also sources Sequence at nanosecond
// granularity (internal/cli/source.go) so rapid syncs do not collide;
// the tracker remains backward-compatible with legacy 1-second-grained
// sequences because it only requires strict monotonic increase.
type SequenceTracker struct {
	mu    sync.Mutex
	seen  map[string]int64
	store SequenceStore
}

// NewSequenceTracker returns a fresh tracker with no persistence. Kept
// for tests and for callers that genuinely want in-memory state. Sink
// code should use NewTrackerFromStore so state survives restart.
func NewSequenceTracker() *SequenceTracker {
	return &SequenceTracker{
		seen:  map[string]int64{},
		store: NewMemorySequenceStore(nil),
	}
}

// NewTrackerFromStore loads existing high-water marks from store and
// returns a tracker that writes through to store on every accepted
// envelope. A corrupt or otherwise unreadable store causes a startup
// error so the sink refuses to boot (a silent fall-through to an
// empty tracker would re-open the replay window the persistence is
// meant to close).
func NewTrackerFromStore(store SequenceStore) (*SequenceTracker, error) {
	if store == nil {
		return nil, fmt.Errorf("nil SequenceStore")
	}
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	if state == nil {
		state = map[string]int64{}
	}
	return &SequenceTracker{seen: state, store: store}, nil
}

// Accept returns true if seq is strictly greater than the highest
// previously seen value for source. Updates the in-memory record AND
// the persistent store on accept; if the persistent store write fails
// the in-memory update is rolled back and Accept returns false to
// avoid acknowledging a write that did not survive a restart.
func (t *SequenceTracker) Accept(source string, seq int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, hadPrev := t.seen[source]
	if hadPrev && seq <= prev {
		return false
	}
	t.seen[source] = seq
	if t.store != nil {
		if err := t.store.Save(t.seen); err != nil {
			// Roll back the in-memory update so the persistent and
			// in-memory state stay consistent across restarts.
			if hadPrev {
				t.seen[source] = prev
			} else {
				delete(t.seen, source)
			}
			return false
		}
	}
	return true
}

// Last returns the highest sequence seen for source, or 0 if none.
func (t *SequenceTracker) Last(source string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[source]
}
