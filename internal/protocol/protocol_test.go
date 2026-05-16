package protocol

import (
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
)

func TestSequenceTrackerAcceptsMonotonic(t *testing.T) {
	st := NewSequenceTracker()
	if !st.Accept("laptop", 100) {
		t.Fatal("first sequence must be accepted")
	}
	if !st.Accept("laptop", 200) {
		t.Fatal("higher sequence must be accepted")
	}
	if st.Accept("laptop", 200) {
		t.Fatal("equal sequence must be rejected (replay)")
	}
	if st.Accept("laptop", 150) {
		t.Fatal("lower sequence must be rejected (replay)")
	}
	if st.Last("laptop") != 200 {
		t.Errorf("expected last=200, got %d", st.Last("laptop"))
	}
}

func TestSequenceTrackerIsolatesSources(t *testing.T) {
	st := NewSequenceTracker()
	st.Accept("laptop-a", 100)
	if !st.Accept("laptop-b", 50) {
		t.Fatal("sequence on a different source should be accepted independently")
	}
	if st.Last("laptop-a") != 100 || st.Last("laptop-b") != 50 {
		t.Errorf("per-source state leaked: a=%d b=%d", st.Last("laptop-a"), st.Last("laptop-b"))
	}
}

func TestBlocklistMatcher_DropsMatchingHosts(t *testing.T) {
	bl := &config.Blocklist{
		Version: 1,
		Domains: []config.BlocklistEntry{
			{Pattern: "%chase.com"},
			{Pattern: "%vanguard.com"},
			{Pattern: "passwords.example.com"},
		},
	}
	m := NewBlocklistMatcher(bl)
	cases := map[string]bool{
		"www.chase.com":          true,
		"chase.com":              true,
		".chase.com":              true,
		"foo.vanguard.com":       true,
		"passwords.example.com":  true,
		"CHASE.COM":              true, // case-insensitive
		"safe.example.com":       false,
		"github.com":             false,
		"":                       false,
	}
	for host, wantBlocked := range cases {
		got := m.MatchesHost(host)
		if got != wantBlocked {
			t.Errorf("MatchesHost(%q) = %v, want %v", host, got, wantBlocked)
		}
	}
}

func TestBlocklistFilter_PassesNonMatchingDropsMatching(t *testing.T) {
	bl := &config.Blocklist{
		Version: 1,
		Domains: []config.BlocklistEntry{{Pattern: "%chase.com"}},
	}
	m := NewBlocklistMatcher(bl)
	cookies := []chrome.Cookie{
		{HostKey: "www.chase.com", Name: "session"},
		{HostKey: "chase.com", Name: "csrf"},
		{HostKey: "github.com", Name: "_gh_sess"},
		{HostKey: "instacart.com", Name: "cart"},
		{HostKey: "chase.com", Name: "second-chase"},
	}
	passed, dropped := m.Filter(cookies)
	if len(passed) != 2 {
		t.Errorf("expected 2 passed, got %d (%+v)", len(passed), passed)
	}
	if dropped["chase.com"] != 2 {
		t.Errorf("expected chase.com dropped=2, got %d", dropped["chase.com"])
	}
	if dropped["www.chase.com"] != 1 {
		t.Errorf("expected www.chase.com dropped=1, got %d", dropped["www.chase.com"])
	}
}

func TestBlocklistMatcher_EmptyAndNilPassEverything(t *testing.T) {
	// Empty blocklist drops nothing; everything passes (sync-all default).
	m := NewBlocklistMatcher(&config.Blocklist{Version: 1})
	if m.MatchesHost("anywhere.com") {
		t.Error("empty blocklist must not match")
	}
	if m.PatternCount() != 0 {
		t.Errorf("expected 0 patterns, got %d", m.PatternCount())
	}
	m2 := NewBlocklistMatcher(nil)
	if m2.MatchesHost("anywhere.com") {
		t.Error("nil blocklist must not match")
	}
}

func TestBlocklistFilter_EmptyBlocklistPassesEverything(t *testing.T) {
	m := NewBlocklistMatcher(&config.Blocklist{Version: 1})
	cookies := []chrome.Cookie{
		{HostKey: "chase.com", Name: "would-have-blocked"},
		{HostKey: "github.com", Name: "ok"},
	}
	passed, dropped := m.Filter(cookies)
	if len(passed) != 2 {
		t.Errorf("expected all 2 cookies to pass with empty blocklist, got %d", len(passed))
	}
	if len(dropped) != 0 {
		t.Errorf("expected no drops with empty blocklist, got %d", len(dropped))
	}
}

func TestMatchLike(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"%chase.com", "www.chase.com", true},
		{"%chase.com", "chase.com", true},
		{"%chase.com", "chase.com.evil.com", false},
		{"exact", "exact", true},
		{"exact", "exactly", false},
		{"%", "anything", true},
		{"%", "", true},
		{"%foo%", "barfoofoo", true},
		{"%foo%", "bar", false},
	}
	for _, c := range cases {
		got := matchLike(c.pattern, c.s)
		if got != c.want {
			t.Errorf("matchLike(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	original := SyncEnvelope{
		ProtocolVersion: Version,
		SourceHostname:  "laptop.tail.ts.net",
		Sequence:        12345,
		Cookies: []chrome.Cookie{
			{HostKey: "x.com", Name: "a", Value: "1", Path: "/", IsSecure: 1, SameSite: 2, ExpiresUTC: 99},
		},
	}
	data, err := jsonMarshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var got SyncEnvelope
	if err := jsonUnmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ProtocolVersion != original.ProtocolVersion ||
		got.SourceHostname != original.SourceHostname ||
		got.Sequence != original.Sequence ||
		len(got.Cookies) != 1 ||
		got.Cookies[0].HostKey != "x.com" ||
		got.Cookies[0].SameSite != 2 {
		t.Errorf("round-trip lost fields: %+v", got)
	}
}
