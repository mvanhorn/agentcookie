package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/pairing"
)

const loopbackIP = "127.0.0.1"

type pairFixture struct {
	*authFixture
	srv *httptest.Server
}

// newPairFixture serves the daemon's 9998 mux on a loopback test server.
// grokbot and instinct are listed; WhoIs maps the loopback caller to node.
func newPairFixture(t *testing.T, node string) *pairFixture {
	t.Helper()
	f := newAuthFixture(t, "teammates:\n  - name: grokbot\n  - name: instinct\n")
	f.whois.nodes[loopbackIP] = node
	bindTeammate(t, f.hub, "grokbot", grokNode, false)
	bindTeammate(t, f.hub, "instinct", "nINSTINCT", false)
	// Drop the fixture's pre-paired grokbot key so pairing must create it.
	if err := keystore.Delete(f.dir, "grokbot"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newPullMux(f.dir, "", f.cache, f.hub))
	t.Cleanup(srv.Close)
	return &pairFixture{authFixture: f, srv: srv}
}

func (f *pairFixture) runSink(code pairing.Code) (*pairing.HandshakeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return pairing.RunSink(ctx, f.srv.URL+"/pair", code, "sink-host")
}

func (f *pairFixture) postStatus(t *testing.T, body []byte) int {
	t.Helper()
	resp, err := http.Post(f.srv.URL+"/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPairRouteClosedWithoutPendingPairing(t *testing.T) {
	f := newPairFixture(t, grokNode)
	if got := f.postStatus(t, []byte(`{}`)); got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with nothing pending", got)
	}
}

// Without teammates.yaml /pair is a 404 exactly as before.
func TestPairRouteAbsentWithoutTeammates(t *testing.T) {
	f := newAuthFixture(t, "")
	srv := httptest.NewServer(newPullMux(f.dir, "", f.cache, f.hub))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/pair", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// Covers R6: a valid code from the bound node pairs, the key is filed
// under the teammate name, and the route closes.
func TestPairRoutePairsFromBoundNode(t *testing.T) {
	f := newPairFixture(t, grokNode)
	code, expires, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(expires); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("code TTL = %s, want 30m", d)
	}
	res, err := f.runSink(code)
	if err != nil {
		t.Fatalf("RunSink: %v", err)
	}
	pk, err := keystore.Load(f.dir, "grokbot")
	if err != nil {
		t.Fatalf("key not filed under teammate name: %v", err)
	}
	if !bytes.Equal(pk.Key, res.Key) || pk.AnnouncedAs != "sink-host" {
		t.Fatalf("stored key mismatch: %+v", pk)
	}
	if rec, _ := f.hub.store.Record("grokbot"); rec.PairedAt.IsZero() {
		t.Fatal("paired_at not recorded")
	}
	select {
	case ev := <-f.hub.Events():
		if ev.Kind != TeammatePaired || ev.Name != "grokbot" || ev.NodeID != grokNode {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("no paired event")
	}
	if got := f.postStatus(t, []byte(`{}`)); got != http.StatusNotFound {
		t.Fatalf("route should close after pairing, status = %d", got)
	}
	// The new key pulls from the bound node.
	if rec := f.pull(t, string(res.Key), grokIP); rec.Code != http.StatusOK {
		t.Fatalf("pull with the new key = %d", rec.Code)
	}
}

// Covers R9: a valid code from another node is rejected and the pending
// entry stays until expiry.
func TestPairRouteRejectsOtherNode(t *testing.T) {
	f := newPairFixture(t, otherNode)
	code, _, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.runSink(code); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("other node should get 403, got %v", err)
	}
	if _, err := keystore.Load(f.dir, "grokbot"); err == nil {
		t.Fatal("no key may be stored for a wrong-node pairing")
	}
	if len(f.hub.PendingPairings()) != 1 {
		t.Fatal("pending pairing should stay after a wrong-node attempt")
	}
	// The real machine can still use the same code.
	f.whois.mu.Lock()
	f.whois.nodes[loopbackIP] = grokNode
	f.whois.mu.Unlock()
	f.hub.whois.clear() // forget the cached wrong answer
	if _, err := f.runSink(code); err != nil {
		t.Fatalf("bound node with the same code: %v", err)
	}
}

func TestPairRouteRejectsExpiredAndReusedCodes(t *testing.T) {
	f := newPairFixture(t, grokNode)
	clock := newTestClock()
	f.hub.now = clock.Now

	code, _, _ := f.hub.MintPairing("grokbot", grokNode)
	if _, _, err := f.hub.MintPairing("instinct", "nINSTINCT"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runSink(code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := f.runSink(code); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("reused code should be 401, got %v", err)
	}

	stale, _, _ := f.hub.MintPairing("grokbot", grokNode)
	clock.Advance(31 * time.Minute)
	if _, err := f.runSink(stale); err == nil {
		t.Fatal("expired code should be rejected")
	}
}

func TestPairRouteTwoPendingsPairIndependently(t *testing.T) {
	f := newPairFixture(t, grokNode)
	grokCode, _, _ := f.hub.MintPairing("grokbot", grokNode)
	instCode, _, _ := f.hub.MintPairing("instinct", "nINSTINCT")
	if _, err := f.runSink(grokCode); err != nil {
		t.Fatalf("grokbot: %v", err)
	}
	f.whois.mu.Lock()
	f.whois.nodes[loopbackIP] = "nINSTINCT"
	f.whois.mu.Unlock()
	f.hub.whois.clear()
	if _, err := f.runSink(instCode); err != nil {
		t.Fatalf("instinct: %v", err)
	}
	for _, name := range []string{"grokbot", "instinct"} {
		if _, err := keystore.Load(f.dir, name); err != nil {
			t.Fatalf("%s key missing: %v", name, err)
		}
	}
}

func TestPairRouteBodyCapAndRateLimit(t *testing.T) {
	f := newPairFixture(t, grokNode)
	if _, _, err := f.hub.MintPairing("grokbot", grokNode); err != nil {
		t.Fatal(err)
	}
	if got := f.postStatus(t, bytes.Repeat([]byte("a"), 20*1024)); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", got)
	}
	var last int
	for range pairing.AttemptLimit + 1 {
		last = f.postStatus(t, []byte(`{"protocol_version":1,"code":"WRON-GCOD-EXXX"}`))
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("status after brute force = %d, want 429", last)
	}
}

func TestMintPairingRequiresListedBoundTeammate(t *testing.T) {
	f := newPairFixture(t, grokNode)
	if _, _, err := f.hub.MintPairing("nobody", "nX"); err == nil {
		t.Fatal("minting for an unlisted name must fail")
	}
	if _, _, err := f.hub.MintPairing("grokbot", "nNOTBOUND"); err == nil {
		t.Fatal("minting for a node other than the bound one must fail")
	}
}

// Fail closed: a broken teammates.yaml suspends /pair too.
func TestPairRouteSuspendedOnParseError(t *testing.T) {
	f := newPairFixture(t, grokNode)
	code, _, _ := f.hub.MintPairing("grokbot", grokNode)
	writeTeammatesFile(t, f.dir, "teammates:\n  - name: [\n")
	_ = f.hub.Reload()
	if _, err := f.runSink(code); err == nil {
		t.Fatal("pairing must be refused while suspended")
	}
	if _, err := keystore.Load(f.dir, "grokbot"); err == nil {
		t.Fatal("no key may be stored while suspended")
	}
}

// testClock is a race-safe settable clock for hub.now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Doctor's ACL diagnosis needs to know whether a teammate's /pair ever
// reached the laptop: every attempt with a teammate's pending code is
// recorded, success or failure; a code that matches nobody records nothing.
func TestPairRouteRecordsAttempts(t *testing.T) {
	f := newPairFixture(t, otherNode)
	clock := newTestClock()
	f.hub.now = clock.Now
	code, _, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}

	f.postStatus(t, []byte(`{"protocol_version":1,"code":"WRON-GCOD-EXXX"}`))
	for _, name := range []string{"grokbot", "instinct"} {
		if rec, _ := f.hub.store.Record(name); !rec.LastPairAttemptAt.IsZero() {
			t.Fatalf("%s: a code matching no pairing must not record an attempt", name)
		}
	}

	// Refused (wrong node) still counts: the request reached the route.
	clock.Advance(time.Minute)
	failedAt := clock.Now()
	if _, err := f.runSink(code); err == nil {
		t.Fatal("wrong node should be refused")
	}
	rec, _ := f.hub.store.Record("grokbot")
	if !rec.LastPairAttemptAt.Equal(failedAt.UTC()) {
		t.Fatalf("refused attempt: last_pair_attempt_at = %v, want %v", rec.LastPairAttemptAt, failedAt)
	}

	// Success records too.
	f.whois.mu.Lock()
	f.whois.nodes[loopbackIP] = grokNode
	f.whois.mu.Unlock()
	f.hub.whois.clear()
	clock.Advance(time.Minute)
	okAt := clock.Now()
	if _, err := f.runSink(code); err != nil {
		t.Fatalf("bound node: %v", err)
	}
	rec, _ = f.hub.store.Record("grokbot")
	if !rec.LastPairAttemptAt.Equal(okAt.UTC()) {
		t.Fatalf("successful attempt: last_pair_attempt_at = %v, want %v", rec.LastPairAttemptAt, okAt)
	}
	if rec, _ := f.hub.store.Record("instinct"); !rec.LastPairAttemptAt.IsZero() {
		t.Fatal("another teammate's record must not change")
	}
}

// A teammate removed from teammates.yaml after its code was minted is
// refused: no key is filed and its pending code is dropped.
func TestPairRouteRefusesTeammateRemovedAfterMint(t *testing.T) {
	f := newPairFixture(t, grokNode)
	code, _, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}
	writeTeammatesFile(t, f.dir, "teammates:\n  - name: instinct\n")
	if err := f.hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runSink(code); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("removed teammate's /pair should get 403, got %v", err)
	}
	if _, err := keystore.Load(f.dir, "grokbot"); err == nil {
		t.Fatal("a key was filed for a removed teammate")
	}
	for _, p := range f.hub.PendingPairings() {
		if p.Name == "grokbot" {
			t.Fatal("the removed teammate's pending code was not cleared")
		}
	}
}

// A code minted for the previous binding is refused once the record is
// bound to another node, and no key is filed.
func TestPairRouteRefusesReboundRecord(t *testing.T) {
	f := newPairFixture(t, grokNode)
	code, _, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}
	bindTeammate(t, f.hub, "grokbot", "nREBUILT", false)
	if _, err := f.runSink(code); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("/pair for a rebound record should get 403, got %v", err)
	}
	if _, err := keystore.Load(f.dir, "grokbot"); err == nil {
		t.Fatal("a key was filed for a rebound record")
	}
	for _, p := range f.hub.PendingPairings() {
		if p.Name == "grokbot" {
			t.Fatal("the stale pending code was not cleared")
		}
	}
}

// One code used twice at once: exactly one pairing succeeds; the other is
// refused as already used (409), no longer pending (401), or by the
// closed route (404) when the winning request finishes before routing.
func TestPairRouteConcurrentUseOfOneCode(t *testing.T) {
	f := newPairFixture(t, grokNode)
	code, _, err := f.hub.MintPairing("grokbot", grokNode)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = f.runSink(code)
		})
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "404"):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("successes = %d, refusals = %d (errs %v); want exactly one of each", ok, refused, errs)
	}
}
