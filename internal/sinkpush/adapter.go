// Package sinkpush pushes decrypted cookies from the agentcookie sink
// into each PP CLI's local session cache, bypassing the macOS Keychain
// on the read side.
//
// Why this exists: macOS's modern Keychain API (SecItemCopyMatching)
// does not durably honor -A or -T ACL entries for ad-hoc-signed Go
// binaries, which is every PP CLI `go install` produces. Each new
// binary triggers a fresh Always-Allow prompt; each rebuild re-prompts.
// Multi-click NUX is unacceptable for the product.
//
// The fix: agentcookie sink runs inside a LaunchAgent with established
// Keychain access. After each cookie sync, the sink runs every
// registered adapter. Each adapter takes the relevant cookies, formats
// them however its target CLI expects, and writes into the CLI's own
// session-cache file (or invokes the CLI's auth-paste import command).
// The PP CLIs then run from local session data on every invocation --
// they never touch Chrome cookies or Keychain.
//
// One install (one Always-Allow click for agentcookie itself), zero
// further clicks for any registered CLI, forever.
//
// See docs/plans/2026-05-17-007-feat-sink-cli-adapter-cookie-push-plan.md
// for the design rationale and the deferred adapter roadmap.
package sinkpush

import (
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// Adapter pushes the relevant subset of cookies into one PP CLI's
// local session cache. Implementations live in adapter_*.go files in
// this package (one per CLI).
type Adapter interface {
	// Name uniquely identifies this adapter in logs and config. By
	// convention, the same string as the PP CLI binary name
	// (e.g., "instacart-pp-cli").
	Name() string

	// CLIBinary returns the absolute path to the target CLI's
	// installed binary. Used by IsInstalled and (for some adapters)
	// as the exec target for auth-paste-style imports.
	CLIBinary() string

	// IsInstalled reports whether the target CLI is present at
	// CLIBinary's path. RunAll skips adapters whose IsInstalled
	// returns false so a missing CLI is not a runtime error.
	IsInstalled() bool

	// CookieHostPatterns returns the SQLite LIKE patterns the
	// adapter wants its cookies filtered to. Sink applies the
	// filter before invoking Push; the adapter receives ONLY
	// cookies whose host_key matches at least one pattern.
	//
	// Returning nil or an empty slice means "give me every cookie"
	// -- a deliberate signal for adapters that want the full set
	// (rare; almost always a single pattern).
	CookieHostPatterns() []string

	// Push writes the filtered cookies into the target CLI's
	// session cache. Strategies vary per adapter (exec auth-paste,
	// write session.json, etc.). Errors are returned as-is for
	// the caller to log; non-fatal -- one failed adapter does
	// not stop other adapters from running.
	Push(cookies []chrome.Cookie) error
}

// Result is one adapter's outcome from a RunAll pass.
type Result struct {
	// Name carries the adapter's Name for log/status output.
	Name string

	// Skipped is true when the adapter's CLI was not installed
	// or its host filter matched zero cookies. Skipped is NOT
	// an error condition.
	Skipped bool

	// SkippedReason is a short human-readable explanation when
	// Skipped is true. Empty when Skipped is false.
	SkippedReason string

	// Pushed is the count of cookies the adapter wrote on
	// success. Zero when the adapter was skipped or errored.
	Pushed int

	// Invalid is the count of cookies dropped by the validator
	// before reaching Push: cookies whose Name, Value, or HostKey
	// failed the safety check in validate.go. A non-zero Invalid
	// is interesting on its own (a source pushing garbage) but
	// does NOT itself fail the adapter.
	Invalid int

	// Err is the underlying error when Push failed. Nil on
	// success or skip.
	Err error
}

// OK reports whether the result was a successful push (or a benign
// skip). False only for actual Push errors.
func (r Result) OK() bool {
	return r.Err == nil
}

// FilterByHostPatterns applies the same SQLite-LIKE host filtering that
// RunAll applies before an adapter's Push. Exported for callers that
// invoke an adapter's Push directly (e.g. the cmux-sync local loop)
// rather than through RunAll, so they honor the adapter's
// CookieHostPatterns identically. An empty patterns slice returns the
// full set.
func FilterByHostPatterns(cookies []chrome.Cookie, patterns []string) []chrome.Cookie {
	return filterByHostPatterns(cookies, patterns)
}

// filterByHostPatterns returns the subset of cookies whose host_key
// matches at least one of the patterns. Pattern matching follows
// SQLite LIKE semantics: '%' matches any sequence; bare hostnames
// match exact-equals. An empty patterns slice returns the full set.
func filterByHostPatterns(cookies []chrome.Cookie, patterns []string) []chrome.Cookie {
	if len(patterns) == 0 {
		return cookies
	}
	out := make([]chrome.Cookie, 0, len(cookies))
	for _, c := range cookies {
		for _, p := range patterns {
			if matchLike(c.HostKey, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// matchLike implements a small subset of SQLite LIKE: '%' matches
// any sequence of characters; everything else is literal. The
// match is case-sensitive (Chrome host_key values are lowercase by
// convention).
//
// Implemented inline rather than via regexp so adapters can declare
// patterns like "%instacart.com" without worrying about regex
// metacharacter escapes. SQLite LIKE is the established mental
// model from chrome.ReadCookiesForHost.
func matchLike(s, pattern string) bool {
	// Normalize: SQLite LIKE on no-wildcard pattern is exact match.
	if !strings.Contains(pattern, "%") {
		return s == pattern
	}
	// Walk segments split by '%'. Each segment must appear in s in
	// order. Anchors at start/end are implied by the wildcard's
	// position.
	segments := strings.Split(pattern, "%")
	pos := 0
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		var idx int
		if i == 0 {
			// First segment must be a prefix of s.
			if !strings.HasPrefix(s[pos:], seg) {
				return false
			}
			idx = pos
		} else {
			idx = strings.Index(s[pos:], seg)
			if idx < 0 {
				return false
			}
			idx += pos
		}
		pos = idx + len(seg)
	}
	// Final segment was non-empty? It must be a suffix of s.
	last := segments[len(segments)-1]
	if last != "" && !strings.HasSuffix(s, last) {
		return false
	}
	return true
}
