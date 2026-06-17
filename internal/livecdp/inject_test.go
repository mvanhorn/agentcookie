package livecdp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// TestBuildCookieParams_HostPrefix is the load-bearing case: __Host-
// cookies must carry NO Domain, plus forced Secure + Path "/". This is the
// exact shaping that fixed GitHub's __Host-user_session_same_site, which
// the cold path dropped by setting a Domain.
func TestBuildCookieParams_HostPrefix(t *testing.T) {
	c := chrome.Cookie{HostKey: "github.com", Name: "__Host-user_session_same_site", Value: "tok", Path: "/", IsSecure: 1, IsHTTPOnly: 1}
	got := BuildCookieParams([]chrome.Cookie{c})
	if len(got) != 1 {
		t.Fatalf("want 1 param, got %d", len(got))
	}
	p := got[0]
	if p.Domain != "" {
		t.Errorf("__Host- cookie must have empty Domain, got %q", p.Domain)
	}
	if !p.Secure {
		t.Errorf("__Host- cookie must be Secure")
	}
	if p.Path != "/" {
		t.Errorf("__Host- cookie must have Path /, got %q", p.Path)
	}
	if p.URL == "" {
		t.Errorf("__Host- cookie must have a URL")
	}
}

// TestBuildCookieParams_HostOnly: a host-only cookie (host_key without a
// leading dot) is scoped via URL and carries no Domain.
func TestBuildCookieParams_HostOnly(t *testing.T) {
	c := chrome.Cookie{HostKey: "github.com", Name: "user_session", Value: "v", Path: "/", IsSecure: 1, IsHTTPOnly: 1}
	p := BuildCookieParams([]chrome.Cookie{c})[0]
	if p.Domain != "" {
		t.Errorf("host-only cookie must have empty Domain, got %q", p.Domain)
	}
	if p.URL != "https://github.com/" {
		t.Errorf("host-only URL: got %q", p.URL)
	}
}

// TestBuildCookieParams_DomainCookie: a domain cookie (leading dot) keeps
// Domain WITH its leading dot so CDP stores it as a subdomain-scoped cookie
// rather than host-only.
func TestBuildCookieParams_DomainCookie(t *testing.T) {
	c := chrome.Cookie{HostKey: ".github.com", Name: "_octo", Value: "v", Path: "/", IsSecure: 1}
	p := BuildCookieParams([]chrome.Cookie{c})[0]
	if p.Domain != ".github.com" {
		t.Errorf("domain cookie keeps the leading dot for subdomain scope: got %q", p.Domain)
	}
	if p.URL != "https://github.com/" {
		t.Errorf("domain cookie URL: got %q", p.URL)
	}
}

// TestBuildCookieParams_HTTPOnlyPersistent proves the CDP path carries the
// exact class Playwright's addCookies rejected (httpOnly + persistent) --
// the real session cookies.
func TestBuildCookieParams_HTTPOnlyPersistent(t *testing.T) {
	c := chrome.Cookie{HostKey: "github.com", Name: "user_session", Value: "v", Path: "/", IsSecure: 1, IsHTTPOnly: 1, ExpiresUTC: 13363527432123456}
	p := BuildCookieParams([]chrome.Cookie{c})[0]
	if !p.HTTPOnly {
		t.Errorf("HTTPOnly must be preserved")
	}
	if p.Expires == nil {
		t.Errorf("persistent cookie must have non-nil Expires")
	}
}

// TestBuildCookieParams_SessionCookie: ExpiresUTC 0 -> nil Expires.
func TestBuildCookieParams_SessionCookie(t *testing.T) {
	c := chrome.Cookie{HostKey: ".github.com", Name: "csrf", Value: "x", Path: "/", IsSecure: 1, ExpiresUTC: 0}
	p := BuildCookieParams([]chrome.Cookie{c})[0]
	if p.Expires != nil {
		t.Errorf("session cookie must have nil Expires")
	}
}

func TestBuildCookieParams_SameSite(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{{-1, ""}, {0, "None"}, {1, "Lax"}, {2, "Strict"}, {99, ""}}
	for _, tc := range cases {
		// IsSecure:1 so the None->Lax insecure downgrade doesn't fire here;
		// this test covers the raw mapping. Downgrade is tested separately.
		c := chrome.Cookie{HostKey: "x.com", Name: "n", Value: "v", SameSite: tc.in, IsSecure: 1}
		got := string(BuildCookieParams([]chrome.Cookie{c})[0].SameSite)
		if got != tc.want {
			t.Errorf("samesite %d: got %q want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildCookieParams_InsecureNoneDowngrade: a SameSite=None cookie that
// is not Secure would be rejected by Chrome; we downgrade it to Lax so it
// survives instead of silently vanishing.
func TestBuildCookieParams_InsecureNoneDowngrade(t *testing.T) {
	insecureNone := chrome.Cookie{HostKey: "x.com", Name: "n", Value: "v", SameSite: 0, IsSecure: 0}
	if got := string(BuildCookieParams([]chrome.Cookie{insecureNone})[0].SameSite); got != "Lax" {
		t.Errorf("insecure None should downgrade to Lax, got %q", got)
	}
	secureNone := chrome.Cookie{HostKey: "x.com", Name: "n", Value: "v", SameSite: 0, IsSecure: 1}
	if got := string(BuildCookieParams([]chrome.Cookie{secureNone})[0].SameSite); got != "None" {
		t.Errorf("secure None should stay None, got %q", got)
	}
}

// TestBuildCookieParams_SkipsInvalid: rows missing a name or host are
// dropped rather than producing a malformed param Chrome would reject.
func TestBuildCookieParams_SkipsInvalid(t *testing.T) {
	in := []chrome.Cookie{
		{HostKey: "github.com", Name: "", Value: "v"},
		{HostKey: "", Name: "n", Value: "v"},
		{HostKey: "github.com", Name: "ok", Value: "v"},
	}
	got := BuildCookieParams(in)
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("expected only the valid cookie, got %d params", len(got))
	}
}

func TestBuildCookieParams_Empty(t *testing.T) {
	if got := BuildCookieParams(nil); len(got) != 0 {
		t.Errorf("nil input -> empty params, got %d", len(got))
	}
}

// TestInject_LiveLogin is the regression form of the 2026-06-05 manual
// proof: inject a cookie into a live headless Chrome, navigate to a local
// server, and assert the browser sent the cookie. Gated behind
// AGENTCOOKIE_LIVE_CDP_TEST because it launches a real Chrome (not
// CI-default). Run with: AGENTCOOKIE_LIVE_CDP_TEST=1 go test ./internal/livecdp/
func TestInject_LiveLogin(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 to run the live-Chrome injection test")
	}

	var mu sync.Mutex
	var sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("ac_live_test"); err == nil {
			mu.Lock()
			sawCookie = ck.Value
			mu.Unlock()
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL) // http://127.0.0.1:PORT

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", "new"),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
		)...)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
	defer tcancel()

	// Non-secure host-only cookie for the loopback http server.
	cookie := chrome.Cookie{HostKey: u.Hostname(), Name: "ac_live_test", Value: "logged-in", Path: "/", IsSecure: 0}
	if err := Inject(ctx, []chrome.Cookie{cookie}); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if sawCookie != "logged-in" {
		t.Fatalf("server did not receive injected cookie; got %q (host=%s)", sawCookie, strings.TrimSpace(u.Hostname()))
	}
}
