package protocol

import (
	"fmt"
	"maps"
	"strings"
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
	if !validSequenceComponent(source) {
		return false
	}
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

// AcceptDestination checks v3 replay in its destination namespace. The shared
// migration floor is fixed before the first v3 accept, not lazily seeded from
// an already advanced legacy mark: two destinations can arrive out of order.
// The v3 marker detects a legacy writer advancing the file after rollback.
// All additional keys remain int64 so older binaries retain the same format.
func (t *SequenceTracker) AcceptDestination(source, destination string, seq int64) bool {
	if destination == "" {
		return t.Accept(source, seq)
	}
	if !validSequenceComponent(source) || !validSequenceComponent(destination) || destination[0] == '@' {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	next := make(map[string]int64, len(t.seen)+3)
	maps.Copy(next, t.seen)
	floorKey, markerKey := source+"/@floor", source+"/@v3"
	floor := next[floorKey]
	if legacy := next[source]; legacy > next[markerKey] && legacy > floor {
		floor = legacy
	}
	key := source + "/" + destination
	last := max(floor, next[key])
	if seq <= last {
		return false
	}
	next[floorKey] = floor
	next[key] = seq
	if seq > next[source] {
		next[source] = seq
	}
	next[markerKey] = next[source]
	if t.store != nil {
		if err := t.store.Save(next); err != nil {
			return false
		}
	}
	t.seen = next
	return true
}
func validSequenceComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
