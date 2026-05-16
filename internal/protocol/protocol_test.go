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

func TestAllowlistMatcher(t *testing.T) {
	al := &config.Allowlist{
		Version: 1,
		Domains: []config.AllowlistEntry{
			{Pattern: "%instacart.com"},
			{Pattern: "%granola.so"},
			{Pattern: "exact.example.com"},
		},
	}
	m := NewAllowlistMatcher(al)
	cases := map[string]bool{
		"www.instacart.com": true,
		"instacart.com":     true,
		".instacart.com":    true,
		"foo.granola.so":    true,
		"exact.example.com": true,
		"INSTACART.COM":     true, // case-insensitive
		"not-allowed.com":   false,
		"":                  false,
	}
	for host, want := range cases {
		got := m.MatchesHost(host)
		if got != want {
			t.Errorf("MatchesHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestAllowlistFilterCountsDropped(t *testing.T) {
	al := &config.Allowlist{
		Version: 1,
		Domains: []config.AllowlistEntry{{Pattern: "%instacart.com"}},
	}
	m := NewAllowlistMatcher(al)
	cookies := []chrome.Cookie{
		{HostKey: "www.instacart.com", Name: "session"},
		{HostKey: "instacart.com", Name: "csrf"},
		{HostKey: "evil.com", Name: "tracker"},
		{HostKey: "another-bad.com", Name: "ad"},
		{HostKey: "evil.com", Name: "second-tracker"},
	}
	accepted, dropped := m.Filter(cookies)
	if len(accepted) != 2 {
		t.Errorf("expected 2 accepted, got %d", len(accepted))
	}
	if dropped["evil.com"] != 2 {
		t.Errorf("expected evil.com dropped=2, got %d", dropped["evil.com"])
	}
	if dropped["another-bad.com"] != 1 {
		t.Errorf("expected another-bad.com dropped=1, got %d", dropped["another-bad.com"])
	}
}

func TestAllowlistMatcherEmpty(t *testing.T) {
	// Empty allowlist rejects everything (defense in depth: no allowlist
	// means no opt-ins, so nothing flows).
	m := NewAllowlistMatcher(&config.Allowlist{Version: 1})
	if m.MatchesHost("anything.com") {
		t.Error("empty allowlist must not match")
	}
	m2 := NewAllowlistMatcher(nil)
	if m2.MatchesHost("anything.com") {
		t.Error("nil allowlist must not match")
	}
}

func TestMatchLike(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"%instacart.com", "www.instacart.com", true},
		{"%instacart.com", "instacart.com", true},
		{"%instacart.com", "instacart.com.evil.com", false},
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
	// Sanity: JSON marshal + unmarshal preserves all fields, including the
	// cookies slice with all flags.
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
