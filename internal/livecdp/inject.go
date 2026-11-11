// Package livecdp injects plaintext cookies into a LIVE, already-running
// Chromium over the DevTools Protocol -- the mechanism that actually
// logs a Chromium agent browser (browser-use, vercel-labs agent-browser)
// into the user's sites.
//
// This is deliberately distinct from internal/cdp, which seeds a COLD
// on-disk profile by spawning its own headless Chrome. The cold path is
// dead for this use case on macOS Chrome 127+: cookies written into a
// profile's SQLite cannot be decrypted on a normal cold launch (App-Bound
// Encryption key mismatch), so Chrome drops every one on load. Verified
// 2026-06-05: a freshly seeded profile loaded 0 of 13 GitHub cookies.
//
// Live injection sidesteps encryption-at-rest entirely: Network.setCookies
// places plaintext cookies directly into the running browser's in-memory
// cookie store, exactly as cmux-sync does for cmux's WebKit store. Verified
// 2026-06-05: plaintext cookies pushed this way logged the test browser into
// GitHub as the user. Cookie values arrive here already decrypted and already
// App-Bound-prefix-stripped by the source pipeline (internal/chrome/read.go);
// this package never touches the Keychain.
package livecdp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// Inject runs Network.setCookies on an already-connected chromedp context
// (a browser, target, or page session). The caller owns connection and
// target selection; this just shapes and sets. A nil/empty cookie slice is
// a no-op so callers can inject unconditionally.
func Inject(ctx context.Context, cookies []chrome.Cookie) error {
	params := BuildCookieParams(cookies)
	if len(params) == 0 {
		return nil
	}
	if err := chromedp.Run(ctx, network.SetCookies(params)); err != nil {
		return fmt.Errorf("livecdp.Inject: Network.setCookies (%d cookies): %w", len(params), err)
	}
	return nil
}

// BuildCookieParams shapes decrypted chrome.Cookie rows into CDP
// CookieParams for live injection. The shaping is the corrected,
// verified-working form -- it differs from internal/cdp.buildCookieParam
// and is shared by the cold path:
//
//   - __Host--prefixed cookies carry NO Domain. Chrome hard-rejects a
//     __Host- cookie that has a Domain attribute, which silently dropped
//     GitHub's __Host-user_session_same_site on the cold path. We set URL,
//     force Path "/" and Secure, and omit Domain.
//   - Host-only cookies (host_key without a leading dot) also carry no
//     Domain -- they are scoped to the exact host via URL. Setting Domain
//     would silently widen them to all subdomains.
//
// Domain cookies (host_key with a leading dot) keep Domain WITH its leading
// dot plus a synthesized URL so Chrome applies the same relaxed validation a
// real Set-Cookie navigation would. The dot is load-bearing: CDP
// Network.setCookie stores a dot-less Domain host-only, which narrows a
// parent-domain cookie to its apex and breaks delivery to app subdomains.
//
// The batch is ordered least-recently-used first and trimmed to
// MaxInjectCookies; see MaxInjectCookies for why both are load-bearing.
func BuildCookieParams(cookies []chrome.Cookie) []*network.CookieParam {
	ordered := slices.Clone(cookies)
	slices.SortStableFunc(ordered, func(a, b chrome.Cookie) int {
		return cmp.Compare(a.LastAccessUTC, b.LastAccessUTC)
	})
	params := make([]*network.CookieParam, 0, len(ordered))
	now := injectNow()
	for _, c := range ordered {
		// Chrome cannot keep an expired cookie, so it must not take a
		// usable cookie's slot under the cap.
		if exp := expiresEpoch(c.ExpiresUTC); exp != nil && !exp.Time().After(now) {
			continue
		}
		if p := buildParam(c); p != nil {
			params = append(params, p)
		}
	}
	if len(params) > MaxInjectCookies {
		params = params[len(params)-MaxInjectCookies:]
	}
	return params
}

// injectNow is the clock BuildCookieParams checks expiry against.
var injectNow = time.Now

// HasAccessTimes reports whether any cookie carries a last-access time, so
// the cap keeps the most recently used cookies. Without one (the CDP
// source), the cap keeps the last MaxInjectCookies in input order.
func HasAccessTimes(cookies []chrome.Cookie) bool {
	return slices.ContainsFunc(cookies, func(c chrome.Cookie) bool { return c.LastAccessUTC != 0 })
}

// MaxInjectCookies is the most cookies one injection pushes. Chrome's
// cookie store (net::CookieMonster) holds at most 3300 cookies; past that it
// garbage-collects down to 3000 by evicting the least-recently-accessed
// ones. Injected cookies get last-access = time set, so in an oversized
// Storage.setCookies batch the FIRST cookies set are evicted by the later
// ones, and the call still reports success. Verified 2026-10-11 against
// headless Chrome 154: a 4868-cookie batch left 3062 cookies, and GitHub's
// session cookies, sitting early in the batch, were gone (the browser then
// redirected github.com/settings to /login). Pushing at most 3000, ordered
// least-recently-used first (source last_access_utc), keeps the cookies the
// user actually uses and lets Chrome evict its own stale cookies instead.
const MaxInjectCookies = 3000

func buildParam(c chrome.Cookie) *network.CookieParam { return BuildCookieParam(c) }

// BuildCookieParam is the single cookie shaping contract shared by cold and
// live injection. It preserves domain scope and both CHIPS ancestor values.
func BuildCookieParam(c chrome.Cookie) *network.CookieParam {
	if c.Name == "" || c.HostKey == "" {
		return nil
	}
	p := &network.CookieParam{
		Name:     c.Name,
		Value:    c.Value,
		URL:      synthesizeURL(c),
		Path:     c.Path,
		Secure:   c.IsSecure == 1,
		HTTPOnly: c.IsHTTPOnly == 1,
		Expires:  expiresEpoch(c.ExpiresUTC),
	}
	if p.Path == "" {
		p.Path = "/"
	}

	switch {
	case strings.HasPrefix(c.Name, "__Host-"):
		// __Host- invariants: Secure, Path "/", host-only (no Domain).
		p.Secure = true
		p.Path = "/"
		// Domain left empty.
	case strings.HasPrefix(c.HostKey, "."):
		// Domain cookie: valid for subdomains. The leading dot is
		// load-bearing for CDP Network.setCookie -- a Domain WITHOUT a
		// leading dot is stored host-only (apex only), so a parent-domain
		// session cookie (e.g. ".example.com") never reaches the app's
		// subdomain (e.g. app.example.com) and the agent browser lands
		// logged out. Keep the dot to preserve subdomain scope.
		p.Domain = c.HostKey
	default:
		// Host-only cookie: scoped to the exact host via URL, no Domain.
	}

	// SameSite=None requires Secure -- Chrome rejects a None cookie that
	// isn't Secure, silently dropping it. Downgrade an insecure None to
	// Lax so the cookie survives rather than vanishing. Real auth cookies
	// are Secure (so this never touches them); it rescues misconfigured rows.
	ss := sameSiteToCDP(c.SameSite)
	if ss == network.CookieSameSiteNone && !p.Secure {
		ss = network.CookieSameSiteLax
	}
	p.SameSite = ss
	if c.TopFrameSiteKey != "" {
		if !p.Secure || (c.HasCrossSiteAncestor != 0 && c.HasCrossSiteAncestor != 1) {
			return nil
		}
		p.PartitionKey = &network.CookiePartitionKey{TopLevelSite: c.TopFrameSiteKey, HasCrossSiteAncestor: c.HasCrossSiteAncestor == 1}
	}
	return p
}

// synthesizeURL builds a request-URI for a cookie from its host_key, scheme,
// and path so Chrome applies relaxed (navigation-equivalent) validation.
func synthesizeURL(c chrome.Cookie) string {
	host := strings.TrimPrefix(c.HostKey, ".")
	if host == "" {
		return ""
	}
	scheme := "https"
	if c.IsSecure == 0 {
		scheme = "http"
	}
	path := c.Path
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path
}

// sameSiteToCDP maps Chrome's numeric SameSite encoding to the CDP enum.
// -1/unknown emits empty so chromedp omits the field and Chrome uses its
// own unspecified-default rather than forcing Lax (which rejects
// originally cross-site cookies).
func sameSiteToCDP(s int) network.CookieSameSite {
	switch s {
	case 0:
		return network.CookieSameSiteNone
	case 1:
		return network.CookieSameSiteLax
	case 2:
		return network.CookieSameSiteStrict
	default:
		return ""
	}
}

// expiresEpoch converts Chrome's microseconds-since-1601 expiry to a CDP
// TimeSinceEpoch. ExpiresUTC == 0 means a session cookie -> nil so Chrome
// treats it as session-scoped.
func expiresEpoch(microsSince1601 int64) *cdp.TimeSinceEpoch {
	if microsSince1601 == 0 {
		return nil
	}
	const chromeEpochOffsetSec = 11644473600
	unixSec := float64(microsSince1601)/1e6 - chromeEpochOffsetSec
	te := cdp.TimeSinceEpoch(time.Unix(int64(unixSec), 0))
	return &te
}
