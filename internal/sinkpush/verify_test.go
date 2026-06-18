package sinkpush

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// evalValue wraps an inner {ready,authed} JSON the way cmux's browser.eval
// response carries it: under a "value" string field.
func evalValue(ready, authed bool) string {
	inner := `{"ready":` + boolStr(ready) + `,"authed":` + boolStr(authed) + `}`
	b, _ := json.Marshal(map[string]any{"value": inner})
	return string(b)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestVerify_Authenticated(t *testing.T) {
	f := &fakeCmux{evalOuts: []string{evalValue(true, true)}}
	a := newTestCmux(f, nil)

	results := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].State != AuthYes {
		t.Errorf("state = %v, want AuthYes", results[0].State)
	}
	if f.closeCalls != 1 {
		t.Errorf("probe surface must be closed, closeCalls = %d", f.closeCalls)
	}
}

func TestVerify_NotAuthenticatedGivesGuidance(t *testing.T) {
	f := &fakeCmux{evalOuts: []string{evalValue(true, false)}}
	a := newTestCmux(f, nil)

	res := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})[0]
	if res.State != AuthNo {
		t.Errorf("state = %v, want AuthNo", res.State)
	}
	if res.Detail == "" {
		t.Error("AuthNo result must carry actionable guidance")
	}
}

func TestVerify_EvalErrorIsUnknownNotFatal(t *testing.T) {
	f := &fakeCmux{evalErrs: []error{errors.New("boom")}}
	a := newTestCmux(f, nil)

	res := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})[0]
	if res.State != AuthUnknown {
		t.Errorf("state = %v, want AuthUnknown on eval error", res.State)
	}
	// Even on error the surface is closed (no leak).
	if f.closeCalls != 1 {
		t.Errorf("probe surface must be closed even on error, closeCalls = %d", f.closeCalls)
	}
}

func TestVerify_SurfaceOpenFailureIsUnknown(t *testing.T) {
	f := &fakeCmux{addErrs: []error{errors.New("no workspace")}}
	a := newTestCmux(f, nil)

	res := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})[0]
	if res.State != AuthUnknown {
		t.Errorf("state = %v, want AuthUnknown when surface cannot open", res.State)
	}
	// No surface opened means none to close.
	if f.closeCalls != 0 {
		t.Errorf("no surface should be closed when open failed, closeCalls = %d", f.closeCalls)
	}
}

func TestVerify_RetriesUntilReady(t *testing.T) {
	// First eval reports not-ready (page still loading), second reports
	// ready+authed. Verifier must poll past the not-ready response.
	f := &fakeCmux{evalOuts: []string{evalValue(false, false), evalValue(true, true)}}
	a := newTestCmux(f, nil)

	res := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})[0]
	if res.State != AuthYes {
		t.Errorf("state = %v, want AuthYes after retry", res.State)
	}
	if f.evalIdx < 2 {
		t.Errorf("expected at least 2 eval attempts, got %d", f.evalIdx)
	}
}

func TestVerify_TimesOutToUnknown(t *testing.T) {
	// Page never finishes loading: every eval reports not-ready.
	f := &fakeCmux{evalOuts: []string{evalValue(false, false)}}
	a := newTestCmux(f, nil)

	res := a.Verify([]VerifySpec{{Host: "github.com", URL: "https://github.com/", Predicate: Predicate{Kind: metaPresent, Arg: "user-login"}}})[0]
	if res.State != AuthUnknown {
		t.Errorf("state = %v, want AuthUnknown on load timeout", res.State)
	}
	if f.evalIdx != verifyMaxAttempts {
		t.Errorf("expected %d eval attempts before timeout, got %d", verifyMaxAttempts, f.evalIdx)
	}
}

func TestVerify_MultipleHostsIndependent(t *testing.T) {
	f := &fakeCmux{evalOuts: []string{evalValue(true, true)}}
	a := newTestCmux(f, nil)

	specs := []VerifySpec{
		{Host: "a.example", URL: "https://a.example/", Predicate: Predicate{Kind: selectorPresent, Arg: "#x"}},
		{Host: "b.example", URL: "https://b.example/", Predicate: Predicate{Kind: selectorPresent, Arg: "#y"}},
	}
	results := a.Verify(specs)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].Host != "a.example" || results[1].Host != "b.example" {
		t.Errorf("results out of order: %q, %q", results[0].Host, results[1].Host)
	}
}

func TestVerifySpecsForHosts_Filter(t *testing.T) {
	// Empty filter returns all builtin specs (github.com today).
	if len(VerifySpecsForHosts(nil)) == 0 {
		t.Error("nil filter should return all builtin specs")
	}
	// A matching pattern keeps github.
	if got := VerifySpecsForHosts([]string{"%github.com"}); len(got) != 1 || got[0].Host != "github.com" {
		t.Errorf("github filter: got %v", got)
	}
	// A non-matching pattern drops everything.
	if got := VerifySpecsForHosts([]string{"%example.com"}); len(got) != 0 {
		t.Errorf("non-matching filter should return none, got %v", got)
	}
}

// TestBuiltinVerifySpecsCoverAllBoundSessionHosts guards the drift between the
// classification list (chrome.BoundSessionHosts) and the probe table
// (BuiltinVerifySpecs): a host added to one without the other would otherwise
// silently never get verified. This fails loudly instead.
func TestBuiltinVerifySpecsCoverAllBoundSessionHosts(t *testing.T) {
	specHosts := map[string]bool{}
	for _, s := range BuiltinVerifySpecs() {
		specHosts[s.Host] = true
	}
	for _, h := range chrome.BoundSessionHosts() {
		if !specHosts[h] {
			t.Errorf("bound-session host %q has no BuiltinVerifySpec; add a verification spec (logged-in marker) for it or it will silently never be checked", h)
		}
	}
}

func TestProbeScript_UsesPredicate(t *testing.T) {
	s := probeScript(Predicate{Kind: metaPresent, Arg: "user-login"})
	if !strings.Contains(s, "document.readyState") {
		t.Error("probe script must gate on readyState")
	}
	if !strings.Contains(s, "user-login") {
		t.Error("probe script must embed the predicate arg")
	}
}
