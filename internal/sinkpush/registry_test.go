package sinkpush

import (
	"errors"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// stubAdapter is a configurable test double for Adapter. Each field
// controls one observable behavior so tests can isolate concerns.
type stubAdapter struct {
	name        string
	installed   bool
	patterns    []string
	pushErr     error
	pushed      [][]chrome.Cookie // history of Push calls for assertions
	binaryPath  string
}

func (s *stubAdapter) Name() string                   { return s.name }
func (s *stubAdapter) CLIBinary() string              { return s.binaryPath }
func (s *stubAdapter) IsInstalled() bool              { return s.installed }
func (s *stubAdapter) CookieHostPatterns() []string   { return s.patterns }
func (s *stubAdapter) Push(c []chrome.Cookie) error {
	s.pushed = append(s.pushed, c)
	return s.pushErr
}

func TestRunAll_RunsInRegistrationOrder(t *testing.T) {
	resetForTesting()
	a := &stubAdapter{name: "first", installed: true, patterns: []string{"%example%"}}
	b := &stubAdapter{name: "second", installed: true, patterns: []string{"%example%"}}
	Register(a)
	Register(b)

	results := RunAll([]chrome.Cookie{{HostKey: ".example.com", Name: "x", Value: "1"}})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "first" || results[1].Name != "second" {
		t.Errorf("order: got %q,%q want first,second", results[0].Name, results[1].Name)
	}
}

func TestRunAll_FiltersByHostPattern(t *testing.T) {
	resetForTesting()
	ic := &stubAdapter{name: "instacart", installed: true, patterns: []string{"%instacart%"}}
	ab := &stubAdapter{name: "airbnb", installed: true, patterns: []string{"%airbnb%"}}
	Register(ic)
	Register(ab)

	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "sess", Value: "i1"},
		{HostKey: ".instacart.com", Name: "csrf", Value: "i2"},
		{HostKey: ".airbnb.com", Name: "session", Value: "a1"},
		{HostKey: ".github.com", Name: "user", Value: "g1"},
	}
	results := RunAll(cookies)
	if len(ic.pushed) != 1 || len(ic.pushed[0]) != 2 {
		t.Errorf("instacart: expected 1 Push call with 2 cookies, got %d calls, %v", len(ic.pushed), ic.pushed)
	}
	if len(ab.pushed) != 1 || len(ab.pushed[0]) != 1 {
		t.Errorf("airbnb: expected 1 Push call with 1 cookie, got %d calls, %v", len(ab.pushed), ab.pushed)
	}
	if results[0].Pushed != 2 || results[1].Pushed != 1 {
		t.Errorf("Pushed counts: got %d,%d want 2,1", results[0].Pushed, results[1].Pushed)
	}
}

func TestRunAll_SkipsNotInstalledAdapter(t *testing.T) {
	resetForTesting()
	a := &stubAdapter{name: "missing", installed: false, patterns: []string{"%example%"}}
	Register(a)

	results := RunAll([]chrome.Cookie{{HostKey: ".example.com", Name: "x", Value: "1"}})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Skipped {
		t.Errorf("expected Skipped=true")
	}
	if !strings.Contains(results[0].SkippedReason, "not installed") {
		t.Errorf("SkippedReason: got %q, want substring 'not installed'", results[0].SkippedReason)
	}
	if len(a.pushed) != 0 {
		t.Errorf("Push should not have been called on a not-installed adapter")
	}
}

func TestRunAll_SkipsWhenNoMatchingCookies(t *testing.T) {
	resetForTesting()
	a := &stubAdapter{name: "instacart", installed: true, patterns: []string{"%instacart%"}}
	Register(a)

	results := RunAll([]chrome.Cookie{
		{HostKey: ".github.com", Name: "x", Value: "1"},
	})
	if !results[0].Skipped {
		t.Errorf("expected Skipped=true when host filter excludes everything")
	}
	if !strings.Contains(results[0].SkippedReason, "no matching cookies") {
		t.Errorf("SkippedReason: got %q", results[0].SkippedReason)
	}
	if len(a.pushed) != 0 {
		t.Errorf("Push should not have been called when filter set is empty")
	}
}

func TestRunAll_OneAdapterFailureDoesNotBlockOthers(t *testing.T) {
	resetForTesting()
	bad := &stubAdapter{name: "broken", installed: true, patterns: []string{"%example%"}, pushErr: errors.New("simulated")}
	good := &stubAdapter{name: "healthy", installed: true, patterns: []string{"%example%"}}
	Register(bad)
	Register(good)

	results := RunAll([]chrome.Cookie{{HostKey: ".example.com", Name: "x", Value: "1"}})
	if results[0].Err == nil {
		t.Errorf("expected broken adapter to report Err")
	}
	if results[1].Err != nil {
		t.Errorf("healthy adapter unexpectedly errored: %v", results[1].Err)
	}
	if results[1].Pushed != 1 {
		t.Errorf("healthy adapter should have pushed despite the prior failure, got Pushed=%d", results[1].Pushed)
	}
}

func TestRunAll_EmptyRegistryReturnsEmpty(t *testing.T) {
	resetForTesting()
	results := RunAll([]chrome.Cookie{{HostKey: ".example.com", Name: "x", Value: "1"}})
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty registry, got %d", len(results))
	}
}

func TestRunAll_EmptyHostPatternsGetsEveryCookie(t *testing.T) {
	resetForTesting()
	a := &stubAdapter{name: "greedy", installed: true, patterns: nil}
	Register(a)

	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "sess", Value: "i1"},
		{HostKey: ".airbnb.com", Name: "session", Value: "a1"},
		{HostKey: ".github.com", Name: "user", Value: "g1"},
	}
	results := RunAll(cookies)
	if results[0].Pushed != 3 {
		t.Errorf("greedy adapter with nil patterns should get all 3 cookies, got Pushed=%d", results[0].Pushed)
	}
	if len(a.pushed) != 1 || len(a.pushed[0]) != 3 {
		t.Errorf("greedy adapter: expected one Push with 3 cookies, got %d calls", len(a.pushed))
	}
}

func TestResult_OK(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		want bool
	}{
		{"success", Result{Name: "x", Pushed: 1}, true},
		{"skip", Result{Name: "x", Skipped: true, SkippedReason: "not installed"}, true},
		{"error", Result{Name: "x", Err: errors.New("boom")}, false},
	}
	for _, tc := range cases {
		if got := tc.r.OK(); got != tc.want {
			t.Errorf("%s: OK()=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestMatchLike_VariousPatterns(t *testing.T) {
	cases := []struct {
		s, p string
		want bool
	}{
		{"www.instacart.com", "%instacart%", true},
		{".instacart.com", "%instacart%", true},
		{"www.instacart.com", "%instacart.com", true},
		{"www.instacart.com", ".instacart.com", false}, // no wildcard = exact
		{".instacart.com", ".instacart.com", true},     // exact match without wildcard
		{".airbnb.com", "%instacart%", false},
		{".airbnb.com", "%airbnb%", true},
		{"sub.example.airbnb.com", "%airbnb%", true},
		{"airbnb.com", "%airbnb.com", true},
		{"airbnbx.com", "%airbnb.com", false},
		{".prefix.example.com", "%example.com", true},
		{".prefix.example.suffix", "%example.com", false},
		{"anything", "%", true},
	}
	for _, tc := range cases {
		if got := matchLike(tc.s, tc.p); got != tc.want {
			t.Errorf("matchLike(%q, %q) = %v, want %v", tc.s, tc.p, got, tc.want)
		}
	}
}
