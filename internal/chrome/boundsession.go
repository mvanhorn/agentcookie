package chrome

import "strings"

// This file lists the hosts whose post-injection login is worth VERIFYING,
// because for them "cookies delivered" has not always meant "session
// authenticated" and a silent failure is costly to debug.
//
// CORRECTION: an earlier version claimed GitHub's session is bound by the
// server to the originating browser and cannot be transplanted. That was WRONG.
// GitHub authenticates fine from transplanted cookies once they are shaped so
// the sink browser actually SENDS them. A cmux regression had stripped the
// Domain from host-only cookies (cmuxCookieParam), so WebKit STORED
// user_session but never sent it -- delivered yet not authenticated. Verified
// by injecting the same cookies into both Chromium (agent-sync) and cmux's
// WebKit and watching GitHub log in. It is not browser-bound, not DBSC.
//
// The list still earns its keep: these hosts get an empirical auth probe after
// a push (internal/sinkpush.Verify) so a future shaping/send regression is
// caught loudly instead of silently leaving the pane logged out. It never gates
// shipping; all cookies still ship.

// boundSessionHosts (historical name) are registrable-domain suffixes whose
// post-push login is verified empirically. Kept separate from dbscKnownHosts.
var boundSessionHosts = []string{
	"github.com",
}

// BoundSessionHosts returns the hosts whose post-push login is verified. Callers
// use it to drive post-injection auth verification (see internal/sinkpush.Verify).
func BoundSessionHosts() []string {
	out := make([]string, len(boundSessionHosts))
	copy(out, boundSessionHosts)
	return out
}

// IsBoundSessionHost reports whether host (leading dot trimmed, any case)
// equals or is a subdomain of a known browser-bound-session host.
func IsBoundSessionHost(host string) bool {
	return hostMatchesSuffix(host, boundSessionHosts)
}

// hostMatchesSuffix reports whether host (leading dot trimmed, lower-cased)
// equals or is a subdomain of any registrable-domain suffix in suffixes.
// Shared by the DBSC (dbsc.go) and browser-bound-session classifiers so the
// host-matching semantics cannot drift between them.
func hostMatchesSuffix(host string, suffixes []string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "."))
	for _, k := range suffixes {
		if host == k || strings.HasSuffix(host, "."+k) {
			return true
		}
	}
	return false
}

// IsBoundSessionCookie reports whether c is the fingerprint of a browser-bound
// session: a Secure cookie on a known bound-session host. A non-secure cookie
// is never a session credential, so it is never a bound-session marker.
func IsBoundSessionCookie(c Cookie) bool {
	if c.IsSecure == 0 {
		return false
	}
	return IsBoundSessionHost(c.HostKey)
}
