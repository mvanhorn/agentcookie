package agentattach

import "fmt"

// LoggedOutCause is the diagnosed reason an attached agent browser reads
// logged-out for a site -- the answer to the user's "are you sure you're
// logged in?" It turns a vague failure into a specific, fixable cause.
type LoggedOutCause int

const (
	// CauseNone: nothing wrong was detected; the session should carry.
	CauseNone LoggedOutCause = iota
	// CauseBlocklisted: the domain is opted out of sync by the blocklist
	// (intentional; not a bug).
	CauseBlocklisted
	// CauseDebuggingOff: the Chrome debug endpoint is not reachable, so no
	// agent browser can attach.
	CauseDebuggingOff
	// CauseNotWired: the endpoint is up but this agent browser was never
	// wired to it.
	CauseNotWired
	// CauseDBSCBound: the site uses device-bound (DBSC) session cookies,
	// which cannot transfer to a copied/debug profile.
	CauseDBSCBound
)

func (c LoggedOutCause) String() string {
	switch c {
	case CauseBlocklisted:
		return "blocklisted"
	case CauseDebuggingOff:
		return "debugging-off"
	case CauseNotWired:
		return "not-wired"
	case CauseDBSCBound:
		return "dbsc-bound"
	default:
		return "none"
	}
}

// PreflightInput is the observed state used to diagnose a logged-out site.
type PreflightInput struct {
	// Reachable: the Chrome debug endpoint answered.
	Reachable bool
	// Tier: the Chrome policy tier (drives the debugging-off remediation).
	Tier PolicyTier
	// Wired: this agent browser has a launcher pointing at the endpoint.
	Wired bool
	// OnDebugProfile: the attach target is the copied debug profile (where
	// DBSC cannot work), not the real profile.
	OnDebugProfile bool
	// DomainBlocklisted: the domain is excluded by the blocklist.
	DomainBlocklisted bool
	// DomainDBSCBound: the domain's session uses device-bound cookies.
	DomainDBSCBound bool
}

// ClassifyLoggedOut diagnoses the most actionable cause for a site reading
// logged-out, with a one-line remediation. Root causes are checked before
// downstream ones: an intentional blocklist opt-out first, then a dead
// endpoint, then unwired, then a DBSC session that cannot survive a copied
// profile.
func ClassifyLoggedOut(in PreflightInput) (LoggedOutCause, string) {
	switch {
	case in.DomainBlocklisted:
		return CauseBlocklisted, "This domain is excluded by your blocklist, so its session is intentionally not shared. Remove it from blocklist.yaml to sync it."
	case !in.Reachable:
		rem := remediationFor(Discovery{Reachable: false, Tier: in.Tier, Version: 0}, DefaultDebugPort)
		return CauseDebuggingOff, rem
	case !in.Wired:
		return CauseNotWired, "Chrome is attachable but this agent browser isn't wired to it. Run `agentcookie attach --wire`."
	case in.DomainDBSCBound && in.OnDebugProfile:
		return CauseDBSCBound, "This site uses device-bound (DBSC) sessions, which can't transfer to the debug profile. Attach your real Chrome instead: run `agentcookie attach` without --fallback."
	default:
		return CauseNone, ""
	}
}

// Summary renders a one-line cause + remediation for display.
func Summary(c LoggedOutCause, remediation string) string {
	if c == CauseNone {
		return "looks attached -- session should carry"
	}
	return fmt.Sprintf("%s: %s", c, remediation)
}
