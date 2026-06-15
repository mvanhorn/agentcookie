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
	"context"
	"fmt"
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
// in two load-bearing ways the cold path got wrong:
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
func BuildCookieParams(cookies []chrome.Cookie) []*network.CookieParam {
	params := make([]*network.CookieParam, 0, len(cookies))
	for _, c := range cookies {
		if p := buildParam(c); p != nil {
			params = append(params, p)
		}
	}
	return params
}

func buildParam(c chrome.Cookie) *network.CookieParam {
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
