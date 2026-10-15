package cli

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// sinkHeartbeat is how long an unchanged payload goes without being re-sent
// to a sink that already received it. doctor only warns after 24h without a
// push, so 10 minutes keeps status fresh while an idle source stops sending
// the same envelope every 30s.
const sinkHeartbeat = 10 * time.Minute

// sinkDelivery is the last payload a sink accepted: the envelope hash (see
// envelopeHash) and when the sink accepted it.
type sinkDelivery struct {
	hash [sha256.Size]byte
	at   time.Time
}

// sinkDeliveryLog remembers, per sink (peer + URL), the last successful
// delivery of this process. It lives in memory only: a restarted source
// sends everything once, which is what --once and a fresh daemon want.
type sinkDeliveryLog struct {
	mu sync.Mutex
	m  map[string]sinkDelivery
}

func newSinkDeliveryLog() *sinkDeliveryLog {
	return &sinkDeliveryLog{m: map[string]sinkDelivery{}}
}

// sinkDeliveries is the source process's delivery log. Tests replace it so
// one test's deliveries do not suppress another's.
var sinkDeliveries = newSinkDeliveryLog()

func sinkDeliveryKey(peer, rawURL string) string {
	return peer + "\x00" + rawURL
}

// unchanged reports whether the sink already received hash less than
// sinkHeartbeat before now, and if so when.
func (l *sinkDeliveryLog) unchanged(peer, rawURL string, hash [sha256.Size]byte, now time.Time) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	d, ok := l.m[sinkDeliveryKey(peer, rawURL)]
	if !ok || d.hash != hash || now.Sub(d.at) >= sinkHeartbeat {
		return time.Time{}, false
	}
	return d.at, true
}

// record notes a successful delivery. Only a sink that accepted the POST is
// recorded, so a failed push is always re-sent.
func (l *sinkDeliveryLog) record(peer, rawURL string, hash [sha256.Size]byte, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.m[sinkDeliveryKey(peer, rawURL)] = sinkDelivery{hash: hash, at: now}
}

// forget drops a sink's delivery, so its next push is sent whatever the
// payload. A failed POST calls it.
func (l *sinkDeliveryLog) forget(peer, rawURL string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, sinkDeliveryKey(peer, rawURL))
}

// envelopeHash identifies an envelope's content for the unchanged check.
// Sequence is zeroed: every real POST needs a fresh one for the sink's
// replay tracker, so it must not make identical content look new.
//
// Cookie LastAccessUTC is zeroed too, in the hash only (the sent payload
// keeps it). Chrome bumps last_access_utc whenever a request sends a cookie,
// throttled to once a minute per cookie (CookieMonster's 60s access-update
// threshold), and its SQLite store commits pending writes every 30s. With
// any tab open, some access time changes between most 30s baseline ticks,
// which would defeat the check. The sink only uses access times to order
// live CDP injection, and the heartbeat carries fresh ones every 10 minutes.
func envelopeHash(env protocol.SyncEnvelope) ([sha256.Size]byte, error) {
	env.Sequence = 0
	cookies := make([]chrome.Cookie, len(env.Cookies))
	for i, c := range env.Cookies {
		c.LastAccessUTC = 0
		cookies[i] = c
	}
	env.Cookies = cookies
	h := sha256.New()
	if err := json.NewEncoder(h).Encode(env); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
