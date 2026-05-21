package cdp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// chromeStartTimeout caps how long we wait for the headless Chrome to
// boot and the CDP target to attach. A cold spawn on a Mac mini takes
// roughly 1-2s; 15s is generous headroom for sluggish machines.
const chromeStartTimeout = 15 * time.Second

// InjectCookies spawns a headless Chrome via chromedp against the
// agentcookie-owned profile at profileDir, calls Network.SetCookies
// with the synced cookies (stripping the 32-byte Chrome 127+
// App-Bound prefix from each value first), then closes. One-shot per
// /sync: no persistent Chrome between calls. Chrome encrypts its own
// SQLite with its own Safe Storage key; agentcookie never reads
// Chrome's Keychain item on this path.
//
// The profileDir must already exist (the wizard install creates it
// at install time so we fail loud on misconfiguration rather than
// silently creating an unexpected dir at sync time).
func InjectCookies(ctx context.Context, profileDir string, cookies []chrome.Cookie) error {
	if profileDir == "" {
		return fmt.Errorf("cdp.InjectCookies: profileDir is required")
	}
	expanded := expandHome(profileDir)
	if _, err := os.Stat(expanded); err != nil {
		return fmt.Errorf("cdp.InjectCookies: profile dir %q does not exist: %w (run `agentcookie wizard install --as sink` to create it)", profileDir, err)
	}
	if len(cookies) == 0 {
		return nil
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.UserDataDir(expanded),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		// Disable the first-run + welcome experiences so chromedp
		// doesn't hang on a first-launch UI dialog when the profile
		// dir was created fresh by the wizard.
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()

	startCtx, startCancel := context.WithTimeout(allocCtx, chromeStartTimeout)
	defer startCancel()

	chromeCtx, chromeCancel := chromedp.NewContext(startCtx)
	defer chromeCancel()

	params := make([]*network.CookieParam, 0, len(cookies))
	for _, c := range cookies {
		stripped := string(StripAppBoundPrefix([]byte(c.Value)))
		params = append(params, buildCookieParam(c, stripped))
	}

	if err := chromedp.Run(chromeCtx, network.SetCookies(params)); err != nil {
		return fmt.Errorf("cdp.InjectCookies: Network.SetCookies (%d cookies, profile=%s): %w", len(params), profileDir, err)
	}
	return nil
}

// buildCookieParam translates a chrome.Cookie row into a CDP
// CookieParam. The key correctness move is providing a synthesized URL
// for every cookie: Chrome's `Network.setCookies` applies stricter
// validation when only Domain+Path is given (rejecting SameSite=None
// without Secure, HttpOnly with insufficient origin, and mismatched
// host-only semantics). Synthesizing a URL from host_key + path +
// scheme makes Chrome treat the cookie as if it were set by a real
// navigation to that origin, which has near-zero rejection rate.
//
// We pass BOTH URL (for origin validation) and Domain (preserves the
// original leading-dot semantics for subdomain scope). When both are
// provided, CDP uses URL for origin checks and Domain for the
// cookie's Domain attribute.
func buildCookieParam(c chrome.Cookie, value string) *network.CookieParam {
	return &network.CookieParam{
		Name:     c.Name,
		Value:    value,
		URL:      synthesizeCookieURL(c),
		Domain:   c.HostKey,
		Path:     c.Path,
		Secure:   c.IsSecure == 1,
		HTTPOnly: c.IsHTTPOnly == 1,
		SameSite: chromeSameSiteToCDP(c.SameSite),
		Expires:  cookieExpiresEpoch(c.ExpiresUTC),
	}
}

// synthesizeCookieURL builds a request-URI for a cookie from its
// host_key and path. Chrome cookies record host_key as either
// ".example.com" (suffix match, valid for subdomains) or "example.com"
// (exact match, host-only). For URL purposes we always need a real
// hostname, so we strip the leading dot if present.
func synthesizeCookieURL(c chrome.Cookie) string {
	host := c.HostKey
	if len(host) > 0 && host[0] == '.' {
		host = host[1:]
	}
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

// chromeSameSiteToCDP translates Chrome's numeric SameSite encoding
// (stored in cookies.samesite) to the CDP CookieSameSite enum. Without
// this, missing SameSite causes Chrome to default to Lax on the CDP
// path, which rejects cookies that were originally cross-site.
//
// Chrome encoding:
//
//	-1 = unspecified
//	 0 = None
//	 1 = Lax
//	 2 = Strict
func chromeSameSiteToCDP(s int) network.CookieSameSite {
	switch s {
	case 0:
		return network.CookieSameSiteNone
	case 1:
		return network.CookieSameSiteLax
	case 2:
		return network.CookieSameSiteStrict
	default:
		// -1 / unspecified: emit empty so chromedp omits the field and
		// Chrome uses its own default behavior for unspecified.
		return ""
	}
}

// cookieExpiresEpoch converts Chrome's microseconds-since-1601 cookie
// expiry to a chromedp/cdproto.TimeSinceEpoch (seconds-since-Unix
// epoch as a float). Cookies with ExpiresUTC = 0 are session cookies
// and we leave Expires nil so Chrome treats them as session-scoped.
func cookieExpiresEpoch(chromeMicrosSince1601 int64) *cdp.TimeSinceEpoch {
	if chromeMicrosSince1601 == 0 {
		return nil
	}
	// Chrome epoch: 1601-01-01 UTC. Unix epoch: 1970-01-01 UTC.
	// Offset is 11644473600 seconds. Chrome stores microseconds.
	const chromeEpochOffsetSec = 11644473600
	unixSec := float64(chromeMicrosSince1601)/1e6 - chromeEpochOffsetSec
	t := time.Unix(int64(unixSec), 0)
	te := cdp.TimeSinceEpoch(t)
	return &te
}

// expandHome resolves a leading ~/ in a path against the current
// user's home directory.
func expandHome(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
