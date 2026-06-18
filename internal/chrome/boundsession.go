package chrome

import "strings"

// This file models browser-bound web sessions: sites whose logged-in session
// is bound by the SERVER to the originating browser, distinct from DBSC.
//
// DBSC (see dbsc.go) is a Chrome mechanism that binds a session key to the
// source machine's secure hardware; a copied DBSC cookie works until its
// short-lived refresh fails. A browser-bound session is different: the server
// gates the logged-in response on a SameSite/site-context cookie and the
// originating browser, and rejects a transplanted session presented from a
// DIFFERENT browser engine even on the same machine and IP. GitHub is the
// known case (its __Host-user_session_same_site cookie and a
// `vary: Sec-Fetch-Site` response): the cookies copy in and are even sent on
// requests, but the session never authenticates in cmux's WebKit browser.
//
// Cookie copying cannot reconstruct a browser-bound session. The honest fix is
// a native login in the target browser (which binds a fresh session to it), or
// gh CLI for git work. This classification exists ONLY to phrase that guidance
// accurately -- it never gates shipping, and bound-session cookies still ship
// (the non-session cookies remain useful, and a later native login benefits
// from the preference/analytics cookies already present).

// boundSessionHosts are registrable-domain suffixes whose web session is
// server-bound to the originating browser. Kept separate from dbscKnownHosts so
// messaging never mislabels a browser-bound session as DBSC.
var boundSessionHosts = []string{
	"github.com",
}

// BoundSessionHosts returns the known browser-bound-session hosts. Callers use
// it to drive post-injection auth verification (see internal/sinkpush.Verify).
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
