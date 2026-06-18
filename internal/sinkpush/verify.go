package sinkpush

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// This file adds an EMPIRICAL post-injection auth check for the cmux lane.
//
// Delivering cookies is not the same as authenticating. For browser-bound
// sessions (see internal/chrome/boundsession.go, e.g. GitHub) the cookies copy
// in and are even sent on requests, yet the server still serves the logged-out
// page. The only honest way to know whether a delivered session actually
// authenticates is to ASK the site: open a hidden browser surface, navigate to
// it, and evaluate a logged-in marker. That is what Verify does, reusing the
// same cmux surface machinery the injection path uses.
//
// Verification is non-fatal by construction: any rpc error, parse failure, or
// load timeout yields AuthUnknown, never an error that could abort a sync.

// AuthState is the verdict for one host's post-injection session check.
type AuthState int

const (
	// AuthUnknown means the probe could not determine the state (cmux error,
	// timeout, or unparseable response). Never treated as a failure.
	AuthUnknown AuthState = iota
	// AuthYes means the delivered session authenticates: the logged-in marker
	// was present.
	AuthYes
	// AuthNo means the cookies were delivered but the session does NOT
	// authenticate -- the browser-bound-session symptom.
	AuthNo
)

func (s AuthState) String() string {
	switch s {
	case AuthYes:
		return "authenticated"
	case AuthNo:
		return "not authenticated"
	default:
		return "unknown"
	}
}

// predicateKind enumerates the safe, deterministic checks a VerifySpec may run
// in the page's isolated content world. Arbitrary JS is deliberately not
// supported -- only these named shapes.
type predicateKind int

const (
	// metaPresent: a <meta name=ARG> exists with non-empty content.
	metaPresent predicateKind = iota
	// selectorPresent: document.querySelector(ARG) matches an element.
	selectorPresent
	// selectorAbsent: document.querySelector(ARG) matches nothing.
	selectorAbsent
)

// Predicate is a named, parameterized logged-in check.
type Predicate struct {
	Kind predicateKind
	Arg  string
}

// VerifySpec describes how to verify one host's session: navigate to URL and
// evaluate Predicate as the logged-in signal.
type VerifySpec struct {
	Host      string
	URL       string
	Predicate Predicate
}

// VerifyResult is the outcome of probing one host.
type VerifyResult struct {
	Host   string
	State  AuthState
	Detail string // populated for AuthUnknown (why) and AuthNo (guidance)
}

// BuiltinVerifySpecs returns the verification specs for the known
// browser-bound-session hosts (see chrome.BoundSessionHosts). github.com is
// verified by the presence of a non-empty <meta name="user-login">, which
// GitHub renders only for an authenticated request.
func BuiltinVerifySpecs() []VerifySpec {
	specs := make([]VerifySpec, 0, len(chrome.BoundSessionHosts()))
	for _, h := range chrome.BoundSessionHosts() {
		switch h {
		case "github.com":
			specs = append(specs, VerifySpec{
				Host:      "github.com",
				URL:       "https://github.com/",
				Predicate: Predicate{Kind: metaPresent, Arg: "user-login"},
			})
		}
	}
	return specs
}

// VerifySpecsForHosts filters BuiltinVerifySpecs to the hosts that match any of
// the given SQLite-LIKE host patterns (e.g. "%github.com"). An empty/nil filter
// returns all builtin specs.
func VerifySpecsForHosts(patterns []string) []VerifySpec {
	all := BuiltinVerifySpecs()
	if len(patterns) == 0 {
		return all
	}
	out := make([]VerifySpec, 0, len(all))
	for _, s := range all {
		for _, p := range patterns {
			// Reuse the lane's SQLite-LIKE matcher (matchLike, adapter.go) so
			// spec filtering and cookie filtering share one semantics. Lowercase
			// both sides since hosts compare case-insensitively.
			if matchLike(strings.ToLower(s.Host), strings.ToLower(p)) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// verifyMaxAttempts and verifyPollInterval bound how long a single host probe
// waits for the page to finish loading before giving up with AuthUnknown.
const (
	verifyMaxAttempts  = 6
	verifyPollInterval = 1 * time.Second
)

// Verify probes each spec and returns one result per host. It opens a dedicated
// hidden browser surface per host, navigates to the spec URL, polls the
// predicate until the page is loaded (or times out), and closes the surface. It
// never returns an error: a probe that cannot complete reports AuthUnknown.
func (a *CmuxAdapter) Verify(specs []VerifySpec) []VerifyResult {
	out := make([]VerifyResult, 0, len(specs))
	for _, s := range specs {
		out = append(out, a.verifyOne(s))
	}
	return out
}

func (a *CmuxAdapter) verifyOne(s VerifySpec) VerifyResult {
	res := VerifyResult{Host: s.Host, State: AuthUnknown}

	sid, err := a.openProbeSurface(s.URL)
	if err != nil {
		res.Detail = fmt.Sprintf("could not open probe surface: %v", err)
		return res
	}
	defer a.closeProbeSurface(sid)

	script := probeScript(s.Predicate)
	for attempt := range verifyMaxAttempts {
		raw, err := a.run("rpc", "browser.eval", probeEvalParams(sid, script))
		if err != nil {
			res.Detail = fmt.Sprintf("eval error: %v", err)
		} else if ready, authed, perr := parseProbeResult(raw); perr != nil {
			res.Detail = fmt.Sprintf("unparseable probe result: %v", perr)
		} else if ready {
			if authed {
				res.State = AuthYes
				res.Detail = ""
			} else {
				res.State = AuthNo
				res.Detail = boundSessionGuidance(s.Host)
			}
			return res
		} else {
			// Successful eval, page still loading: clear any detail from an
			// earlier transient error so a later timeout reports the timeout,
			// not a stale "eval error" from an attempt the page recovered from.
			res.Detail = ""
		}
		if attempt < verifyMaxAttempts-1 {
			a.sleep(verifyPollInterval)
		}
	}
	if res.Detail == "" {
		res.Detail = "page did not finish loading before timeout"
	}
	return res
}

// openProbeSurface creates a dedicated hidden browser surface already navigated
// to url, and returns its surface:N ref. Distinct from the cached injection
// surface so the probe can be closed without disturbing injection.
func (a *CmuxAdapter) openProbeSurface(url string) (string, error) {
	out, err := a.run("new-surface", "--type", "browser", "--url", url, "--focus", "false")
	if err != nil {
		return "", err
	}
	sid := surfaceRefRE.FindString(out)
	if sid == "" {
		return "", fmt.Errorf("could not parse surface ref from %q", strings.TrimSpace(out))
	}
	return sid, nil
}

// closeProbeSurface best-effort closes a probe surface; errors are ignored
// (a leaked surface is harmless and cmux reclaims it on restart).
func (a *CmuxAdapter) closeProbeSurface(sid string) {
	params, err := json.Marshal(map[string]any{"surface_id": sid})
	if err != nil {
		return
	}
	_, _ = a.run("rpc", "surface.close", string(params))
}

// probeEvalParams builds browser.eval params for a synchronous expression.
func probeEvalParams(sid, script string) string {
	b, _ := json.Marshal(map[string]any{"surface_id": sid, "script": script})
	return string(b)
}

// probeScript returns a synchronous JS expression that evaluates to a JSON
// string {"ready":bool,"authed":bool}. ready gates on document.readyState so a
// not-yet-loaded page is retried rather than reported as logged-out. The
// predicate runs in the isolated content world (shares the DOM); only DOM
// queries are used, never page-world globals.
func probeScript(p Predicate) string {
	return "JSON.stringify({ready: document.readyState === 'complete', authed: !!(" + predicateExpr(p) + ")})"
}

// predicateExpr renders one Predicate as a boolean JS expression. Args are
// JSON-encoded so they are safely quoted inside the expression.
func predicateExpr(p Predicate) string {
	arg, _ := json.Marshal(p.Arg)
	switch p.Kind {
	case metaPresent:
		// non-empty content on <meta name="ARG">. Build the full selector with
		// a double-quoted attribute value (e.g. meta[name="user-login"]) and
		// JSON-encode the whole selector: an unquoted CSS attribute value is
		// only valid for identifier-shaped names and breaks on values with
		// hyphens-at-start, colons, dots, etc.
		sel, _ := json.Marshal("meta[name=" + string(arg) + "]")
		return "(document.querySelector(" + string(sel) + ")||{}).content"
	case selectorPresent:
		return "document.querySelector(" + string(arg) + ")"
	case selectorAbsent:
		return "!document.querySelector(" + string(arg) + ")"
	default:
		return "false"
	}
}

// parseProbeResult unwraps cmux's browser.eval response (which carries the
// evaluated value under "value" as a string) and decodes the inner
// {"ready","authed"} JSON.
func parseProbeResult(raw string) (ready, authed bool, err error) {
	var outer struct {
		Value string `json:"value"`
	}
	if uerr := json.Unmarshal([]byte(raw), &outer); uerr != nil {
		return false, false, uerr
	}
	if outer.Value == "" {
		return false, false, fmt.Errorf("empty eval value")
	}
	var inner struct {
		Ready  bool `json:"ready"`
		Authed bool `json:"authed"`
	}
	if uerr := json.Unmarshal([]byte(outer.Value), &inner); uerr != nil {
		return false, false, uerr
	}
	return inner.Ready, inner.Authed, nil
}

// boundSessionGuidance is the actionable message for a delivered-but-not
// -authenticated session: cookie copy cannot reconstruct a server-bound
// session; log in natively in the cmux browser, or use gh CLI for git work.
func boundSessionGuidance(host string) string {
	return fmt.Sprintf("cookies delivered, but %s binds the session to the origin browser and rejects the transplant; log in to %s once inside the cmux browser, or use the gh CLI for git and PR work", host, host)
}
