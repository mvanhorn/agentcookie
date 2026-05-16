package chromeconn

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// chromeEpochDeltaSeconds converts Chrome's WebKit microsecond epoch
// (1601-01-01) into Unix seconds (1970-01-01) by subtracting the offset.
const chromeEpochDeltaSeconds float64 = 11644473600

// WriteCookies sends a Network.SetCookies batch over the attached chromedp
// context. Returns the number of cookies submitted to Chrome. chromedp's
// SetCookies handles SameSite=None+Secure normalization, partitionKey,
// expiry overflow, and other edge cases that the hand-rolled CDP path
// silently dropped on.
//
// Errors are returned per batch. Callers that want per-cookie failure
// resolution should chunk and retry; see writeCookiesChunked below.
func WriteCookies(ctx context.Context, cookies []chrome.Cookie) (int, error) {
	if len(cookies) == 0 {
		return 0, nil
	}
	params := make([]*network.CookieParam, 0, len(cookies))
	for i := range cookies {
		p := cookieToParam(cookies[i])
		if p == nil {
			continue
		}
		params = append(params, p)
	}
	if len(params) == 0 {
		return 0, nil
	}
	if err := chromedp.Run(ctx, network.SetCookies(params)); err != nil {
		return 0, fmt.Errorf("chromeconn: Network.SetCookies: %w", err)
	}
	return len(params), nil
}

// WriteCookiesChunked submits cookies in batches of chunkSize. On a per-batch
// error, the batch is retried with chunkSize=1 so a single bad cookie does
// not poison the rest. Returns total accepted count and any unrecoverable
// per-cookie failures keyed by cookie host_key.
func WriteCookiesChunked(ctx context.Context, cookies []chrome.Cookie, chunkSize int) (int, map[string]int, error) {
	if chunkSize <= 0 {
		chunkSize = 500
	}
	failures := map[string]int{}
	accepted := 0
	for start := 0; start < len(cookies); start += chunkSize {
		end := start + chunkSize
		if end > len(cookies) {
			end = len(cookies)
		}
		batch := cookies[start:end]
		n, err := WriteCookies(ctx, batch)
		if err == nil {
			accepted += n
			continue
		}
		if len(batch) == 1 {
			failures[batch[0].HostKey]++
			continue
		}
		for i := range batch {
			n2, err2 := WriteCookies(ctx, batch[i:i+1])
			if err2 != nil {
				failures[batch[i].HostKey]++
				continue
			}
			accepted += n2
		}
	}
	return accepted, failures, nil
}

// cookieToParam translates a chrome.Cookie (SQLite-native shape) into a
// network.CookieParam (CDP shape). Returns nil for cookies that cannot be
// represented (empty name and value, missing host).
func cookieToParam(c chrome.Cookie) *network.CookieParam {
	if c.HostKey == "" || c.Name == "" {
		return nil
	}
	p := &network.CookieParam{
		Name:         c.Name,
		Value:        c.Value,
		Domain:       c.HostKey,
		Path:         c.Path,
		Secure:       c.IsSecure != 0,
		HTTPOnly:     c.IsHTTPOnly != 0,
		SameSite:     sameSiteEnum(c.SameSite),
		SourceScheme: sourceSchemeEnum(c.SourceScheme, c.IsSecure != 0),
	}
	if c.SourcePort > 0 {
		p.SourcePort = int64(c.SourcePort)
	}
	// SameSite=None cookies are silently dropped unless Chrome can verify a
	// secure source. Providing an explicit URL lets Chrome derive scheme and
	// port; without it the cookie is rejected. Apply only when the cookie is
	// Secure (the only case where SameSite=None is legal anyway).
	if p.Secure {
		p.URL = "https://" + strings.TrimPrefix(c.HostKey, ".") + c.Path
	}
	if c.HasExpires != 0 && c.ExpiresUTC > 0 {
		expiresUnix := float64(c.ExpiresUTC)/1e6 - chromeEpochDeltaSeconds
		t := cdpTimeSinceEpoch(expiresUnix)
		p.Expires = &t
	}
	return p
}

// cdpTimeSinceEpoch wraps a Unix-seconds float into cdproto's time wrapper.
// Out-of-range floats (very-far-future expirations Chrome SQLite occasionally
// produces) get clamped so chromedp doesn't reject the whole batch.
func cdpTimeSinceEpoch(unixSeconds float64) cdp.TimeSinceEpoch {
	if math.IsNaN(unixSeconds) || math.IsInf(unixSeconds, 0) || unixSeconds < 0 {
		return cdp.TimeSinceEpoch(time.Unix(0, 0))
	}
	if unixSeconds > 1<<62 {
		// Cap at year ~2262; far beyond any reasonable cookie expiry.
		return cdp.TimeSinceEpoch(time.Unix(1<<33, 0))
	}
	whole := int64(unixSeconds)
	frac := unixSeconds - float64(whole)
	return cdp.TimeSinceEpoch(time.Unix(whole, int64(frac*1e9)).UTC())
}

// sourceSchemeEnum maps Chrome SQLite source_scheme int (0=unset, 1=non-secure,
// 2=secure) to cdproto's enum. Chrome silently drops SameSite=None cookies
// whose source scheme is Unset/NonSecure even when Secure=true; we infer
// Secure source scheme from the IsSecure flag as a safety net for cookies
// whose SQLite source_scheme field is empty.
func sourceSchemeEnum(v int, isSecure bool) network.CookieSourceScheme {
	switch v {
	case 1:
		return network.CookieSourceSchemeNonSecure
	case 2:
		return network.CookieSourceSchemeSecure
	default:
		if isSecure {
			return network.CookieSourceSchemeSecure
		}
		return network.CookieSourceSchemeUnset
	}
}

// sameSiteEnum maps Chrome SQLite samesite int to cdproto's enum.
//
//	-1 unspecified -> SameSite zero value (omit from JSON, Chrome picks default)
//	 0 no_restriction -> SameSiteNone
//	 1 lax -> SameSiteLax
//	 2 strict -> SameSiteStrict
func sameSiteEnum(v int) network.CookieSameSite {
	switch v {
	case 0:
		return network.CookieSameSiteNone
	case 1:
		return network.CookieSameSiteLax
	case 2:
		return network.CookieSameSiteStrict
	default:
		return network.CookieSameSite("")
	}
}

