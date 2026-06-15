package protocol

import (
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
)

// BlocklistMatcher checks whether a cookie's host_key matches any of the
// configured patterns and applies the effective cookie policy. v0.3 inverted
// the v0.2 semantic to blocklist-by-default; explicit allowlist mode is
// available without changing omitted-policy behavior.
//
// Patterns mirror SQLite LIKE semantics: '%' is a wildcard, anything else
// matches literally. Source and sink use the same matching rules.
type BlocklistMatcher struct {
	policy   config.CookiePolicy
	patterns []string
}

// NewBlocklistMatcher returns a matcher built from the given blocklist.
// nil or empty blocklist yields a matcher that drops nothing.
func NewBlocklistMatcher(bl *config.Blocklist) *BlocklistMatcher {
	if bl == nil {
		return &BlocklistMatcher{policy: config.CookiePolicyBlocklist}
	}
	patterns := make([]string, 0, len(bl.Domains))
	for _, d := range bl.Domains {
		if d.Pattern != "" {
			patterns = append(patterns, strings.ToLower(d.Pattern))
		}
	}
	return &BlocklistMatcher{policy: bl.PolicyMode(), patterns: patterns}
}

// MatchesHost reports whether host matches at least one configured pattern.
// In blocklist mode, a match means "drop this cookie." In allowlist mode, a
// match means "keep this cookie." Use ShouldSyncHost for policy-aware callers.
func (m *BlocklistMatcher) MatchesHost(host string) bool {
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

// ShouldSyncHost reports whether a host passes the configured policy.
func (m *BlocklistMatcher) ShouldSyncHost(host string) bool {
	if m == nil {
		return true
	}
	matched := m.MatchesHost(host)
	if m.policy == config.CookiePolicyAllowlist {
		return matched
	}
	return !matched
}

// PolicySummary returns the operator-facing policy label.
func (m *BlocklistMatcher) PolicySummary() string {
	if m == nil {
		return "sync-all"
	}
	if m.policy == config.CookiePolicyAllowlist {
		return string(config.CookiePolicyAllowlist)
	}
	if len(m.patterns) == 0 {
		return "sync-all"
	}
	return string(config.CookiePolicyBlocklist)
}

// DropLabel returns a short phrase for filtered-cookie counts.
func (m *BlocklistMatcher) DropLabel() string {
	if m != nil && m.policy == config.CookiePolicyAllowlist {
		return "non-allowlisted"
	}
	return "blocklisted"
}

// Filter returns the cookies that pass (host_key does NOT match the
// blocklist, or host_key DOES match the allowlist) and a map of dropped hosts
// keyed by host_key with the count per host. Drops are logged on the sink for
// observability; the source reports its own drop counts via watcher Stats.
func (m *BlocklistMatcher) Filter(cookies []chrome.Cookie) (passed []chrome.Cookie, droppedHosts map[string]int) {
	droppedHosts = map[string]int{}
	for _, c := range cookies {
		if !m.ShouldSyncHost(c.HostKey) {
			droppedHosts[c.HostKey]++
			continue
		}
		passed = append(passed, c)
	}
	return passed, droppedHosts
}

// PatternCount returns how many host patterns are configured. Surfaced via
// `agentcookie status` so the user sees "sync-all", "blocklist", or
// "allowlist" at a glance.
func (m *BlocklistMatcher) PatternCount() int {
	if m == nil {
		return 0
	}
	return len(m.patterns)
}

// matchLike implements SQLite-style LIKE matching for our pattern language:
// '%' matches any sequence of characters (including empty), all other
// characters match literally. Case-insensitive on the caller's behalf.
func matchLike(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	if pattern[0] == '%' {
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
