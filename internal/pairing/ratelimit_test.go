package pairing

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_BucketCap(t *testing.T) {
	l := newRateLimiter()
	for i := 0; i < AttemptLimit; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Errorf("attempt %d should be refused", AttemptLimit+1)
	}
}

func TestRateLimiter_PerIPIsolation(t *testing.T) {
	l := newRateLimiter()
	for i := 0; i < AttemptLimit; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("a attempt %d should be allowed", i+1)
		}
	}
	// Different IP still has full bucket.
	if !l.allow("5.6.7.8") {
		t.Errorf("first attempt from a fresh IP should be allowed")
	}
}

func TestRateLimiter_Refills(t *testing.T) {
	// Pin time to verify the refill arithmetic.
	now := time.Now()
	l := newRateLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < AttemptLimit; i++ {
		_ = l.allow("1.2.3.4")
	}
	if l.allow("1.2.3.4") {
		t.Fatal("expected limit hit")
	}
	// Advance one refill interval; one more attempt allowed.
	now = now.Add(RefillInterval + 10*time.Millisecond)
	if !l.allow("1.2.3.4") {
		t.Error("expected one refilled token to allow attempt")
	}
}

func TestClientIP_HandlesHostOnly(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:5678", "::1"},
		{"weird-no-port", "weird-no-port"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = c.in
		got := clientIP(r)
		if got != c.want {
			t.Errorf("clientIP(%q) = %q want %q", c.in, got, c.want)
		}
	}
}
