package protocol

import (
	"sync"
)

// SequenceTracker is the sink's in-memory record of the highest Sequence
// number seen per source. Rejects equal-or-lower numbers to defend against
// replay of captured payloads. State is process-local; it resets on restart,
// at which point a stale captured payload could once again be played. That
// trade-off is acceptable for v0.1; durable replay defense lands when the
// envelope grows a nonce or timestamp window.
type SequenceTracker struct {
	mu   sync.Mutex
	seen map[string]int64
}

// NewSequenceTracker returns a fresh tracker.
func NewSequenceTracker() *SequenceTracker {
	return &SequenceTracker{seen: make(map[string]int64)}
}

// Accept returns true if seq is strictly greater than the highest previously
// seen value for source. Updates the record on accept.
func (t *SequenceTracker) Accept(source string, seq int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.seen[source]; ok && seq <= last {
		return false
	}
	t.seen[source] = seq
	return true
}

// Last returns the highest sequence seen for source, or 0 if none.
func (t *SequenceTracker) Last(source string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen[source]
}
