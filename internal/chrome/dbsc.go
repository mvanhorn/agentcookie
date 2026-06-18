package chrome

import (
	"fmt"
	"strings"
	"time"
)

// This file implements a conservative, read-only heuristic for spotting
// cookies that Chrome's Device Bound Session Credentials (DBSC) protocol is
// likely to make useless on a different machine.
//
// DBSC binds a per-session private key to the source machine's secure
// hardware (Secure Enclave on macOS) and issues short-lived cookies that the
// browser silently refreshes by signing a server challenge. A cookie copied
// to the sink works only until that short-lived cookie expires, because the
// sink cannot sign the refresh. agentcookie cannot read a site's DBSC
// `credentials` list from the Cookies SQLite, so this is a proxy signal, not
// a guarantee: it keys on the observable fingerprint (a secure cookie that is
// either on a known DBSC host or carries an unusually short remaining TTL).
//
// The default posture is non-destructive: suspect cookies still ship, paired
// with a warning, so a false positive never breaks a working non-DBSC site.
// Hard-skip is opt-in (see source --skip-dbsc-suspect).

// chromeEpochOffsetMicros is the microsecond gap between Chrome's 1601-01-01
// epoch (used by the cookies expires_utc column) and the Unix 1970-01-01
// epoch.
const chromeEpochOffsetMicros = 11644473600 * 1_000_000

// dbscShortTTL is the remaining-lifetime ceiling below which a secure,
// persistent cookie is treated as DBSC-suspect. DBSC short-lived cookies are
// typically refreshed every 5-10 minutes; 15 minutes is a deliberately
// conservative ceiling that catches them without flagging ordinary cookies.
const dbscShortTTL = 15 * time.Minute

// dbscKnownHosts are registrable-domain suffixes known to use DBSC today
// (Google account / Workspace auth). A secure cookie on one of these is
// suspect regardless of TTL, and its warning points the user at the
// multi-device guidance (sign the sink's Chrome into the same account).
var dbscKnownHosts = []string{
	"google.com",
}

// DBSCDecision is the routing verdict for one cookie under the heuristic.
type DBSCDecision int

const (
	// DBSCShip means the cookie is not DBSC-suspect; ship it normally.
	DBSCShip DBSCDecision = iota
	// DBSCShipWarn means the cookie looks DBSC-bound; ship it but warn that
	// it may stop working on the sink within minutes.
	DBSCShipWarn
	// DBSCSkip means the cookie looks DBSC-bound and skip-mode is on, so it
	// is dropped from the push.
	DBSCSkip
)

func (d DBSCDecision) String() string {
	switch d {
	case DBSCShip:
		return "ship"
	case DBSCShipWarn:
		return "ship+warn"
	case DBSCSkip:
		return "skip"
	default:
		return "unknown"
	}
}

// DBSCResult is the outcome of classifying a batch of cookies.
type DBSCResult struct {
	// Shipped is the cookies that should be pushed to the sink.
	Shipped []Cookie
	// Warned holds one reason string per suspect cookie that was still
	// shipped (warn mode).
	Warned []string
	// Skipped holds one reason string per suspect cookie that was dropped
	// (skip mode).
	Skipped []string
}

// ClassifyDBSC returns the routing verdict for one cookie and a human reason
// when the cookie is suspect. now is the reference wall-clock time; skip
// selects hard-skip mode over the default ship+warn. The function is pure and
// never mutates the cookie.
func ClassifyDBSC(c Cookie, now time.Time, skip bool) (DBSCDecision, string) {
	// DBSC binds session/auth cookies, which are always Secure. A non-secure
	// cookie is never a DBSC credential, so it is never suspect. This is also
	// what keeps ordinary short-lived preference/analytics cookies clean.
	if c.IsSecure == 0 {
		return DBSCShip, ""
	}

	host := strings.ToLower(strings.TrimPrefix(c.HostKey, "."))
	var reason string
	switch {
	case matchesKnownDBSCHost(host):
		reason = fmt.Sprintf("cookie %q on known DBSC host %q is device-bound to the source Mac and will not survive on the sink; sign the sink's Chrome into the same account instead (see README: DBSC)", c.Name, c.HostKey)
	case isShortLived(c, now):
		reason = fmt.Sprintf("secure cookie %q on %q expires within %s; if this site uses DBSC it will not refresh on the sink", c.Name, c.HostKey, dbscShortTTL)
	default:
		return DBSCShip, ""
	}

	if skip {
		return DBSCSkip, reason
	}
	return DBSCShipWarn, reason
}

// ClassifyCookies runs ClassifyDBSC over a batch, returning the cookies to
// ship plus the warn/skip reasons. In warn mode (skip=false) every cookie is
// shipped and suspects are recorded in Warned; in skip mode suspects are
// dropped and recorded in Skipped.
func ClassifyCookies(cookies []Cookie, now time.Time, skip bool) DBSCResult {
	var res DBSCResult
	res.Shipped = make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		decision, reason := ClassifyDBSC(c, now, skip)
		switch decision {
		case DBSCSkip:
			res.Skipped = append(res.Skipped, reason)
		case DBSCShipWarn:
			res.Warned = append(res.Warned, reason)
			res.Shipped = append(res.Shipped, c)
		default:
			res.Shipped = append(res.Shipped, c)
		}
	}
	return res
}

// isShortLived reports whether a persistent cookie expires within dbscShortTTL
// of now. Session cookies (no expiry) and already-expired cookies return
// false: the former are not DBSC short-lived cookies, the latter are handled
// by the normal pipeline.
func isShortLived(c Cookie, now time.Time) bool {
	if c.HasExpires == 0 || c.ExpiresUTC == 0 {
		return false
	}
	exp := chromeTimeToUnix(c.ExpiresUTC)
	if !exp.After(now) {
		return false
	}
	return exp.Sub(now) <= dbscShortTTL
}

// chromeTimeToUnix converts a Chrome expires_utc value (microseconds since
// 1601-01-01 UTC) to a time.Time.
func chromeTimeToUnix(micros int64) time.Time {
	return time.UnixMicro(micros - chromeEpochOffsetMicros).UTC()
}

// matchesKnownDBSCHost reports whether host equals or is a subdomain of a known
// DBSC host. Shares hostMatchesSuffix (boundsession.go) with the bound-session
// classifier so the two host matchers cannot drift apart.
func matchesKnownDBSCHost(host string) bool {
	return hostMatchesSuffix(host, dbscKnownHosts)
}
