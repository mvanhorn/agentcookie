package chrome

import "testing"

func TestIsBoundSessionHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"github.com", true},
		{".github.com", true},     // leading dot trimmed
		{"GitHub.com", true},      // case-insensitive
		{"gist.github.com", true}, // subdomain
		{"google.com", false},     // DBSC host, NOT a bound-session host (no cross-contamination)
		{"notgithub.com", false},  // suffix must be on a dot boundary
		{"example.com", false},    // ordinary host
		{"", false},
	}
	for _, tc := range cases {
		if got := IsBoundSessionHost(tc.host); got != tc.want {
			t.Errorf("IsBoundSessionHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestIsBoundSessionCookie(t *testing.T) {
	// A secure cookie on github.com is a bound-session marker.
	if !IsBoundSessionCookie(Cookie{HostKey: "github.com", Name: "user_session", IsSecure: 1}) {
		t.Error("secure github.com cookie should be a bound-session marker")
	}
	// A non-secure cookie is never a session credential.
	if IsBoundSessionCookie(Cookie{HostKey: "github.com", Name: "tz", IsSecure: 0}) {
		t.Error("non-secure github.com cookie must not be a bound-session marker")
	}
	// A Google (DBSC) cookie is not a bound-session marker -- it routes through
	// the DBSC path instead.
	if IsBoundSessionCookie(Cookie{HostKey: "google.com", Name: "SID", IsSecure: 1}) {
		t.Error("google.com cookie must not classify as bound-session")
	}
	// An ordinary host is neither.
	if IsBoundSessionCookie(Cookie{HostKey: "example.com", Name: "s", IsSecure: 1}) {
		t.Error("example.com cookie must not classify as bound-session")
	}
}

func TestBoundSessionHostsIsCopy(t *testing.T) {
	got := BoundSessionHosts()
	if len(got) == 0 {
		t.Fatal("BoundSessionHosts should not be empty")
	}
	// Mutating the returned slice must not affect the package state.
	got[0] = "mutated.example"
	if BoundSessionHosts()[0] == "mutated.example" {
		t.Error("BoundSessionHosts must return a copy, not the backing slice")
	}
}
