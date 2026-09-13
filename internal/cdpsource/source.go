// Package cdpsource reads cookies from an already-running Chromium instance
// through a loopback-only Chrome DevTools Protocol endpoint. It never opens
// or copies the browser's encrypted SQLite cookie database.
package cdpsource

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

const chromeEpochOffsetSec = 11644473600

// ValidateEndpoint permits only a root HTTP endpoint on loopback. A CDP source
// has browser-control authority, so it must never be pointed at a tailnet or
// public endpoint through configuration.
func ValidateEndpoint(raw string) error {
	if raw == "" {
		return fmt.Errorf("cdp source endpoint is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse cdp source endpoint: %w", err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("cdp source endpoint must use http, got %q", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("cdp source endpoint must be a bare loopback origin")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("cdp source endpoint host is required")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("cdp source endpoint must use a literal loopback IP, got %q", host)
	}
	return nil
}

// Read obtains the current browser cookie jar via CDP. Cookie values remain in
// memory and are returned only to the caller's encrypted AgentCookie transport.
func Read(ctx context.Context, endpoint string) ([]chrome.Cookie, error) {
	if err := ValidateEndpoint(endpoint); err != nil {
		return nil, err
	}
	allocator, cancel := chromedp.NewRemoteAllocator(ctx, strings.TrimSuffix(endpoint, "/")+"/json/version")
	defer cancel()
	browserCtx, browserCancel := chromedp.NewContext(allocator)
	defer browserCancel()

	// Initialize a target before issuing the browser-scoped Storage command.
	// A freshly created chromedp context has no executor until its first Run.
	if err := chromedp.Run(browserCtx); err != nil {
		return nil, fmt.Errorf("initialize cdp source context: %w", err)
	}
	var cookies []*network.Cookie
	if err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		browser := chromedp.FromContext(ctx).Browser
		var err error
		cookies, err = storage.GetCookies().Do(cdp.WithExecutor(ctx, browser))
		return err
	})); err != nil {
		return nil, fmt.Errorf("read cookies via cdp: %w", err)
	}
	out := make([]chrome.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		out = append(out, convertCookie(cookie))
	}
	return out, nil
}

func convertCookie(in *network.Cookie) chrome.Cookie {
	out := chrome.Cookie{
		HostKey:      in.Domain,
		Name:         in.Name,
		Value:        in.Value,
		Path:         in.Path,
		IsSecure:     boolInt(in.Secure),
		IsHTTPOnly:   boolInt(in.HTTPOnly),
		Priority:     priority(in.Priority),
		SameSite:     sameSite(in.SameSite),
		SourceScheme: sourceScheme(in.SourceScheme),
		SourcePort:   int(in.SourcePort),
	}
	if !in.Session && in.Expires >= 0 {
		out.ExpiresUTC = int64((in.Expires + chromeEpochOffsetSec) * 1e6)
		out.HasExpires = 1
		out.IsPersistent = 1
	}
	return out
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func priority(value network.CookiePriority) int {
	switch value {
	case network.CookiePriorityLow:
		return 0
	case network.CookiePriorityMedium:
		return 1
	case network.CookiePriorityHigh:
		return 2
	default:
		return 1
	}
}

func sameSite(value network.CookieSameSite) int {
	switch value {
	case network.CookieSameSiteNone:
		return 0
	case network.CookieSameSiteLax:
		return 1
	case network.CookieSameSiteStrict:
		return 2
	default:
		return -1
	}
}

func sourceScheme(value network.CookieSourceScheme) int {
	switch value {
	case network.CookieSourceSchemeNonSecure:
		return 1
	case network.CookieSourceSchemeSecure:
		return 2
	default:
		return 0
	}
}
