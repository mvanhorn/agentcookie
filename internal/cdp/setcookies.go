package cdp

import (
	"context"
	"fmt"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// chromeEpochDeltaSeconds converts Chrome's WebKit microsecond epoch
// (1601-01-01) into Unix seconds (1970-01-01) by subtracting the offset.
const chromeEpochDeltaSeconds float64 = 11644473600

// CookieParam matches Chrome's CDP Network.CookieParam type (only the fields
// we populate). Storage.setCookies accepts an array of these.
type CookieParam struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

// SetCookies sends Storage.setCookies via the browser-level CDP connection.
// Cookies set this way are visible to all pages in the default browser
// context immediately, regardless of whether any tab is open for the domain.
func SetCookies(ctx context.Context, conn *Conn, cookies []chrome.Cookie) (int, error) {
	if len(cookies) == 0 {
		return 0, nil
	}
	params := make([]CookieParam, 0, len(cookies))
	for _, c := range cookies {
		cp := CookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.HostKey,
			Path:     c.Path,
			Secure:   c.IsSecure != 0,
			HTTPOnly: c.IsHTTPOnly != 0,
			SameSite: sameSiteString(c.SameSite),
		}
		if c.HasExpires != 0 && c.ExpiresUTC > 0 {
			cp.Expires = float64(c.ExpiresUTC)/1e6 - chromeEpochDeltaSeconds
		}
		params = append(params, cp)
	}
	args := map[string]interface{}{"cookies": params}
	if err := conn.Call(ctx, "Storage.setCookies", args, nil); err != nil {
		return 0, fmt.Errorf("Storage.setCookies: %w", err)
	}
	return len(params), nil
}

// sameSiteString maps Chrome SQLite samesite int to CDP's enum strings. Empty
// string means "omit the field"; CDP treats absent SameSite as the platform
// default.
func sameSiteString(v int) string {
	switch v {
	case 0:
		return "None"
	case 1:
		return "Lax"
	case 2:
		return "Strict"
	default:
		// Unspecified (-1) or unknown: let Chrome decide.
		return ""
	}
}
