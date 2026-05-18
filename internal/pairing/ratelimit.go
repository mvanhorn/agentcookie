package pairing

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// AttemptLimit caps wrong-code attempts per remote IP within a pairing
// window. Reaching the cap returns 429 until refill or the listener exits.
const AttemptLimit = 5

// RefillInterval is how long a single token takes to regenerate. Set
// such that a legitimate user typing or pasting the code never hits
// the limit, but an automated guesser spends meaningful wall time.
const RefillInterval = 500 * time.Millisecond

// rateLimiter is a small per-IP token bucket. Reset state lives only
// for the lifetime of the pairing listener (~10 minutes by default),
// so memory growth is bounded by attacker IP cardinality during that
// window. Persistent storage is unnecessary.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time // injectable for tests
}

type bucket struct {
	tokens  int
	updated time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// allow consumes a token for ip. Returns true if the caller may proceed.
// The first call from a previously-unseen ip starts with a full bucket
// (AttemptLimit-1 remaining after this call).
func (l *rateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[ip]
	if !ok {
		// Pre-fill, then consume one for this call.
		l.buckets[ip] = &bucket{tokens: AttemptLimit - 1, updated: now}
		return true
	}
	// Refill based on elapsed time.
	elapsed := now.Sub(b.updated)
	refill := int(elapsed / RefillInterval)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > AttemptLimit {
			b.tokens = AttemptLimit
		}
		b.updated = b.updated.Add(time.Duration(refill) * RefillInterval)
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}

// clientIP extracts the remote IP portion of r.RemoteAddr. Returns the
// raw RemoteAddr on parse failure so the rate limiter still works
// against the same key consistently.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
