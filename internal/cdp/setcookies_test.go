package cdp

import (
	"testing"

	"github.com/chromedp/cdproto/network"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// TestSynthesizeCookieURL covers the host_key -> URL translation
// that's load-bearing for CDP injection. Without it, Chrome rejects
// a large fraction of cookies (~64% global, >90% on instacart.com
// in the 2026-05-21 dry-run).
func TestSynthesizeCookieURL(t *testing.T) {
	cases := []struct {
		name string
		c    chrome.Cookie
		want string
	}{
		{
			name: "leading dot host_key, secure, path /",
			c:    chrome.Cookie{HostKey: ".instacart.com", Path: "/", IsSecure: 1},
			want: "https://instacart.com/",
		},
		{
			name: "host-only key, secure, deep path",
			c:    chrome.Cookie{HostKey: "auth.example.com", Path: "/oauth/callback", IsSecure: 1},
			want: "https://auth.example.com/oauth/callback",
		},
		{
			name: "non-secure cookie -> http scheme",
			c:    chrome.Cookie{HostKey: "localhost", Path: "/", IsSecure: 0},
			want: "http://localhost/",
		},
		{
			name: "empty path defaults to /",
			c:    chrome.Cookie{HostKey: ".github.com", Path: "", IsSecure: 1},
			want: "https://github.com/",
		},
		{
			name: "empty host_key returns empty (caller falls through)",
			c:    chrome.Cookie{HostKey: "", Path: "/", IsSecure: 1},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := synthesizeCookieURL(tc.c)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChromeSameSiteToCDP covers all four Chrome numeric SameSite
// values. The mapping matters because Chrome's CDP SetCookies path
// defaults missing SameSite to Lax, which rejects cookies that were
// originally cross-site (SameSite=None in Chrome's encoding).
func TestChromeSameSiteToCDP(t *testing.T) {
	cases := []struct {
		in   int
		want network.CookieSameSite
	}{
		{-1, ""},
		{0, network.CookieSameSiteNone},
		{1, network.CookieSameSiteLax},
		{2, network.CookieSameSiteStrict},
		{99, ""}, // unknown -> empty so chromedp omits the field
	}
	for _, tc := range cases {
		got := chromeSameSiteToCDP(tc.in)
		if got != tc.want {
			t.Errorf("chromeSameSiteToCDP(%d): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildCookieParam_HappyPath confirms the pieces fit together:
// every relevant chrome.Cookie field maps to a CookieParam field
// without dropping data.
func TestBuildCookieParam_HappyPath(t *testing.T) {
	c := chrome.Cookie{
		HostKey:    ".instacart.com",
		Name:       "_session",
		Value:      "should-be-replaced",
		Path:       "/api/v2",
		IsSecure:   1,
		IsHTTPOnly: 1,
		SameSite:   0, // None
		ExpiresUTC: 13363527432123456,
	}
	got := buildCookieParam(c, "post-prefix-strip-value")
	if got.URL != "https://instacart.com/api/v2" {
		t.Errorf("URL: got %q", got.URL)
	}
	if got.Domain != ".instacart.com" {
		t.Errorf("Domain: got %q", got.Domain)
	}
	if got.Path != "/api/v2" {
		t.Errorf("Path: got %q", got.Path)
	}
	if got.Value != "post-prefix-strip-value" {
		t.Errorf("Value: got %q", got.Value)
	}
	if !got.Secure {
		t.Errorf("Secure: got false")
	}
	if !got.HTTPOnly {
		t.Errorf("HTTPOnly: got false")
	}
	if got.SameSite != network.CookieSameSiteNone {
		t.Errorf("SameSite: got %q", got.SameSite)
	}
	if got.Expires == nil {
		t.Errorf("Expires: got nil, want non-nil for persistent cookie")
	}
}

// TestBuildCookieParam_SessionCookie confirms that a session cookie
// (ExpiresUTC=0) leaves Expires nil so Chrome treats it as
// session-scoped.
func TestBuildCookieParam_SessionCookie(t *testing.T) {
	c := chrome.Cookie{HostKey: ".github.com", Name: "csrf", Value: "x", Path: "/", IsSecure: 1, ExpiresUTC: 0}
	got := buildCookieParam(c, "x")
	if got.Expires != nil {
		t.Errorf("session cookie should have nil Expires, got non-nil")
	}
}
