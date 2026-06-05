// Package agentattach brokers a Chromium agent browser (browser-use,
// vercel-labs agent-browser) onto the user's real Chrome over the Chrome
// DevTools Protocol, so the agent browser shares the user's live cookies,
// localStorage, and device-bound sessions instead of running an empty
// profile. It is the Chromium counterpart to the cmux WebKit injection
// loop in internal/sinkpush: cmux cannot CDP-attach (it is WebKit), so it
// gets injected cookies; Chromium agent browsers attach to the real
// session instead of receiving a copy.
//
// This file owns Chrome version detection and the policy tier that
// decides whether the user's REAL default profile can be attached.
// Chrome 136 stopped honoring --remote-debugging-port on the default
// user-data-dir (a malware-hardening change: an open debug port on the
// everyday profile lets any local process drain the cookie jar). Chrome
// 144+ reintroduced a user-gated path to the real profile via
// chrome://inspect#remote-debugging / autoConnect. The tier drives which
// remediation the broker prints. macOS-only, matching internal/chromepaths.
package agentattach

import (
	"os/exec"
	"strconv"
	"strings"
)

// PolicyTier describes how a given Chrome version permits CDP attach to
// the user's real default profile.
type PolicyTier int

const (
	// TierUnknown is the conservative default when the Chrome version
	// cannot be determined. Callers treat it like TierCustomDirOnly: do
	// not assume the real profile is attachable; prefer the fallback.
	TierUnknown PolicyTier = iota
	// TierLegacy (<136): --remote-debugging-port is honored on the
	// default profile, so the real profile can be attached directly.
	TierLegacy
	// TierCustomDirOnly (136..143): Chrome refuses the port flag on the
	// default user-data-dir. Only a non-default dir is debuggable, so the
	// real default profile is NOT attachable -- use the debug-profile
	// fallback.
	TierCustomDirOnly
	// TierAutoConnect (>=144): the default profile is attachable via the
	// user-gated chrome://inspect#remote-debugging / autoConnect path.
	TierAutoConnect
)

func (t PolicyTier) String() string {
	switch t {
	case TierLegacy:
		return "legacy"
	case TierCustomDirOnly:
		return "custom-dir-only"
	case TierAutoConnect:
		return "auto-connect"
	default:
		return "unknown"
	}
}

// TierForVersion maps a Chrome major version to its CDP-attach policy
// tier. A non-positive major (unknown) maps to TierUnknown.
func TierForVersion(major int) PolicyTier {
	switch {
	case major <= 0:
		return TierUnknown
	case major < 136:
		return TierLegacy
	case major < 144:
		return TierCustomDirOnly
	default:
		return TierAutoConnect
	}
}

// ParseChromeMajor extracts the Chrome major version from a CDP
// /json/version "Browser" string such as "Chrome/148.0.7778.217" or
// "HeadlessChrome/120.0.6099.71". It returns (major, true) on success
// and (0, false) for any unrecognized or empty input. Robust to a bare
// version ("148.0.7778.217") with no product prefix.
func ParseChromeMajor(browser string) (int, bool) {
	s := strings.TrimSpace(browser)
	if s == "" {
		return 0, false
	}
	// Take the segment after the last "/" when a product prefix is present
	// ("Chrome/148.0..." -> "148.0..."); otherwise use the whole string.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	// The major is everything up to the first ".".
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	major, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || major <= 0 {
		return 0, false
	}
	return major, true
}

// macChromeBinary is the default-profile Chrome executable on macOS. It
// is a var so tests can point installedChromeMajor at a stub.
var macChromeBinary = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// installedChromeMajor returns the major version of the Chrome installed
// on this Mac, or 0 if it cannot be determined. It runs `Chrome
// --version` (which prints "Google Chrome 148.0.7778.217" without
// launching a window) rather than reading the binary plist, so the same
// code path works regardless of plist format. Used only when the live
// CDP endpoint is unreachable and we still want to pick the right
// remediation.
var installedChromeMajor = func() int {
	out, err := exec.Command(macChromeBinary, "--version").Output()
	if err != nil {
		return 0
	}
	// `--version` prints e.g. "Google Chrome 148.0.7778.217". Take the
	// last whitespace token (the version) before parsing the major.
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	major, ok := ParseChromeMajor(fields[len(fields)-1])
	if !ok {
		return 0
	}
	return major
}
