package cdp

import (
	"context"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"os"
	"path/filepath"
	"time"

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
// with the already-decrypted synced cookies, then closes. One-shot per
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

	// Cookie values arrive here ALREADY stripped of Chrome 127+'s
	// App-Bound 32-byte SHA256(host_key) prefix -- internal/chrome/read.go
	// runs the defensive stripAppBoundPrefix on the source side before
	// shipping. A second strip on this path (as we did in v0.12.0-beta.3)
	// silently lopped 32 bytes off real cookie values for any cookie
	// longer than the prefix, and Chrome rejected the mangled cookies on
	// Network.setCookies -- producing the 64% drop rate measured in the
	// 2026-05-21 dry-run. We pass through c.Value verbatim.
	params := make([]*network.CookieParam, 0, len(cookies))
	for _, c := range cookies {
		if p := buildCookieParam(c, c.Value); p != nil {
			params = append(params, p)
		}
	}

	if err := chromedp.Run(chromeCtx, network.SetCookies(params)); err != nil {
		return fmt.Errorf("cdp.InjectCookies: Network.SetCookies (%d cookies, profile=%s): %w", len(params), profileDir, err)
	}

	// Gracefully close the browser and WAIT for it to exit before
	// returning. Cookies set via Network.setCookies live in Chrome's
	// in-memory cookie store and are only flushed to the profile's SQLite
	// on a clean shutdown. The deferred context cancels below SIGKILL the
	// process, which loses un-flushed writes nondeterministically and
	// leaves the profile's SingletonLock held -- which corrupts the seed
	// when a persistent Chrome is then launched on the same dir (the
	// debug-profile fallback) and can silently drop cookies on the sink.
	// chromedp.Cancel performs the graceful Browser.close and waits for
	// the process to exit, guaranteeing the flush and lock release.
	if err := chromedp.Cancel(chromeCtx); err != nil {
		return fmt.Errorf("cdp.InjectCookies: graceful close (flush): %w", err)
	}
	return nil
}

// buildCookieParam shares the live lane's shaping, including the leading dot
// required to preserve parent-domain scope in Chrome. Values are plaintext.
func buildCookieParam(c chrome.Cookie, value string) *network.CookieParam {
	c.Value = value
	return livecdp.BuildCookieParam(c)
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
