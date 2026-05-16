package protocol

import (
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
)

// AllowlistMatcher checks whether a cookie's host_key matches any of the
// configured patterns. Patterns mirror SQLite LIKE semantics: '%' is a
// wildcard, anything else matches literally. This keeps the source and sink
// using the same matching rules.
type AllowlistMatcher struct {
	patterns []string
}

// NewAllowlistMatcher returns a matcher built from the given allowlist.
// Patterns are normalized to lowercase for case-insensitive matching.
func NewAllowlistMatcher(al *config.Allowlist) *AllowlistMatcher {
	if al == nil {
		return &AllowlistMatcher{}
	}
	patterns := make([]string, 0, len(al.Domains))
	for _, d := range al.Domains {
		if d.Pattern != "" {
			patterns = append(patterns, strings.ToLower(d.Pattern))
		}
	}
	return &AllowlistMatcher{patterns: patterns}
}

// MatchesHost reports whether host matches at least one configured pattern.
func (m *AllowlistMatcher) MatchesHost(host string) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	h := strings.ToLower(host)
	for _, p := range m.patterns {
		if matchLike(p, h) {
			return true
		}
	}
	return false
}

// Filter returns only the cookies whose HostKey matches the allowlist. Stats
// returns the count of accepted and dropped cookies for logging.
func (m *AllowlistMatcher) Filter(cookies []chrome.Cookie) (accepted []chrome.Cookie, droppedHosts map[string]int) {
	droppedHosts = map[string]int{}
	for _, c := range cookies {
		if m.MatchesHost(c.HostKey) {
			accepted = append(accepted, c)
		} else {
			droppedHosts[c.HostKey]++
		}
	}
	return accepted, droppedHosts
}

// matchLike implements SQLite-style LIKE matching for our pattern language:
// '%' matches any sequence of characters (including empty), all other
// characters match literally. Case-insensitive on the caller's behalf.
func matchLike(pattern, s string) bool {
	// Recursive scan; pattern length is small (single hostname), so no risk.
	if pattern == "" {
		return s == ""
	}
	if pattern[0] == '%' {
		// Try matching '%' to each suffix of s.
		rest := pattern[1:]
		if matchLike(rest, s) {
			return true
		}
		for i := 0; i < len(s); i++ {
			if matchLike(rest, s[i+1:]) {
				return true
			}
		}
		return false
	}
	if len(s) == 0 {
		return false
	}
	if pattern[0] == s[0] {
		return matchLike(pattern[1:], s[1:])
	}
	return false
}
