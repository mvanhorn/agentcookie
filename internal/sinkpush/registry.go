package sinkpush

import (
	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// registry is the package-level slice of registered adapters. A slice
// (not a map) so iteration order is stable -- adapters run in the
// order they were registered, which makes sink log output predictable
// across runs.
var registry []Adapter

// Register adds an adapter to the package registry. Built-in adapter
// implementations call Register from their package init function; tests
// may also call it to install stub adapters. Registration is
// append-only; there is no Deregister, by design.
func Register(a Adapter) {
	registry = append(registry, a)
}

// All returns a copy of the current registry. Callers iterate the
// returned slice; mutating it does not affect the package registry.
func All() []Adapter {
	out := make([]Adapter, len(registry))
	copy(out, registry)
	return out
}

// resetForTesting clears the registry. Test-only helper; not exported.
func resetForTesting() {
	registry = nil
}

// RunAll invokes every registered adapter against the given cookie
// slice. For each adapter:
//
//  1. If IsInstalled returns false, the adapter is skipped (Result with
//     Skipped=true, no error).
//  2. Otherwise, cookies are filtered by the adapter's host patterns.
//     If the filtered slice is empty, the adapter is skipped with
//     SkippedReason="no matching cookies".
//  3. Otherwise, Push is invoked. The Push error (if any) lands in
//     Result.Err. Success returns Pushed=len(filtered).
//
// One adapter's failure does NOT stop subsequent adapters from running.
// The Result slice has one entry per registered adapter, in registration
// order, so callers can log a full report.
func RunAll(cookies []chrome.Cookie) []Result {
	adapters := All()
	results := make([]Result, 0, len(adapters))
	for _, a := range adapters {
		results = append(results, runOne(a, cookies))
	}
	return results
}

func runOne(a Adapter, cookies []chrome.Cookie) Result {
	r := Result{Name: a.Name()}

	if !a.IsInstalled() {
		r.Skipped = true
		r.SkippedReason = "CLI not installed"
		return r
	}

	filtered := filterByHostPatterns(cookies, a.CookieHostPatterns())
	if len(filtered) == 0 {
		r.Skipped = true
		r.SkippedReason = "no matching cookies"
		return r
	}

	valid := make([]chrome.Cookie, 0, len(filtered))
	for _, c := range filtered {
		if err := Validate(c); err != nil {
			r.Invalid++
			continue
		}
		valid = append(valid, c)
	}
	if len(valid) == 0 {
		r.Skipped = true
		r.SkippedReason = "all cookies failed validation"
		return r
	}

	if err := a.Push(valid); err != nil {
		r.Err = err
		return r
	}
	r.Pushed = len(valid)
	return r
}
