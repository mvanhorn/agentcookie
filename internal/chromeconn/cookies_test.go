package chromeconn

import (
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestCookieToParam_Happy(t *testing.T) {
	// instacart-shaped session cookie: SameSite=None, Secure, persistent expiry.
	c := chrome.Cookie{
		HostKey:    ".instacart.com",
		Name:       "_instacart_session_id",
		Value:      "abc.def.ghi",
		Path:       "/",
		IsSecure:   1,
		IsHTTPOnly: 1,
		SameSite:   0, // None
		HasExpires: 1,
		ExpiresUTC: 13380163200000000, // 2025-01-01 UTC in WebKit microseconds (Unix 1735689600 + delta 11644473600, times 1e6)
	}
	p := cookieToParam(c)
	if p == nil {
		t.Fatal("expected non-nil CookieParam")
	}
	if p.Name != c.Name || p.Value != c.Value || p.Domain != c.HostKey {
		t.Errorf("identity fields wrong: %+v", p)
	}
	if !p.Secure || !p.HTTPOnly {
		t.Errorf("secure/httponly flags wrong: %+v", p)
	}
	if p.SameSite != network.CookieSameSiteNone {
		t.Errorf("samesite: got %q want None", p.SameSite)
	}
	if p.Expires == nil {
		t.Fatal("expected non-nil Expires for persistent cookie")
	}
	got := time.Time(*p.Expires).UTC()
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Expires: got %s want %s", got, want)
	}
}

func TestCookieToParam_SessionCookie(t *testing.T) {
	c := chrome.Cookie{
		HostKey: "github.com", Name: "logged_in", Value: "yes",
		HasExpires: 0,
	}
	p := cookieToParam(c)
	if p == nil {
		t.Fatal("nil")
	}
	if p.Expires != nil {
		t.Errorf("session cookie should have nil Expires, got %v", p.Expires)
	}
}

func TestCookieToParam_SameSiteMapping(t *testing.T) {
	cases := []struct {
		sql  int
		want network.CookieSameSite
	}{
		{0, network.CookieSameSiteNone},
		{1, network.CookieSameSiteLax},
		{2, network.CookieSameSiteStrict},
		{-1, network.CookieSameSite("")},
	}
	for _, tc := range cases {
		c := chrome.Cookie{HostKey: "x.com", Name: "n", SameSite: tc.sql}
		p := cookieToParam(c)
		if p == nil {
			t.Fatalf("nil for samesite %d", tc.sql)
		}
		if p.SameSite != tc.want {
			t.Errorf("samesite %d: got %q want %q", tc.sql, p.SameSite, tc.want)
		}
	}
}

func TestCookieToParam_RejectsEmpty(t *testing.T) {
	cases := []chrome.Cookie{
		{Name: "n"},                  // no host
		{HostKey: "x.com"},           // no name
		{HostKey: "", Name: ""},      // both empty
	}
	for i, c := range cases {
		if p := cookieToParam(c); p != nil {
			t.Errorf("case %d: expected nil, got %+v", i, p)
		}
	}
}

func TestCookieToParam_FarFutureExpiryClamped(t *testing.T) {
	c := chrome.Cookie{
		HostKey: "x.com", Name: "n", HasExpires: 1,
		ExpiresUTC: 1 << 62, // absurd
	}
	p := cookieToParam(c)
	if p == nil || p.Expires == nil {
		t.Fatal("expected non-nil")
	}
	// Just verify no panic and we got SOME time back. Exact clamp value is
	// implementation detail; we only assert it's well past 1970.
	if time.Time(*p.Expires).Year() < 2000 {
		t.Errorf("clamped time should be in the future, got %s", time.Time(*p.Expires))
	}
}
