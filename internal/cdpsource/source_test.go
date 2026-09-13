package cdpsource

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

func TestValidateEndpointAcceptsLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:9230",
		"http://[::1]:9230",
	} {
		if err := ValidateEndpoint(endpoint); err != nil {
			t.Fatalf("ValidateEndpoint(%q): %v", endpoint, err)
		}
	}
}

func TestValidateEndpointRejectsNonLoopbackOrUnsafeURLs(t *testing.T) {
	for _, endpoint := range []string{
		"",
		"https://127.0.0.1:9230",
		"http://localhost:9230",
		"http://100.91.16.115:9230",
		"http://example.com:9230",
		"http://127.0.0.1:9230/json/version",
		"http://127.0.0.1:9230?token=secret",
	} {
		if err := ValidateEndpoint(endpoint); err == nil {
			t.Errorf("ValidateEndpoint(%q) succeeded, want error", endpoint)
		}
	}
}

func TestReadLiveChrome(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 to run live CDP source test")
	}

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	owned, err := livecdp.LaunchOwnedChrome(ctx, "", dir, 9412, true)
	if err != nil {
		t.Fatalf("LaunchOwnedChrome: %v", err)
	}
	defer owned.Close()

	allocator, allocatorCancel := chromedp.NewRemoteAllocator(ctx, owned.Endpoint)
	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocator)
	defer browserCancel()
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatalf("connect to owned Chrome: %v", err)
	}
	if err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetCookie("agentcookie_cdp_source", "test-value").WithDomain("example.com").WithPath("/").Do(ctx)
	})); err != nil {
		t.Fatalf("set test cookie: %v", err)
	}

	cookies, err := Read(ctx, owned.Endpoint)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, cookie := range cookies {
		if cookie.HostKey == "example.com" && cookie.Name == "agentcookie_cdp_source" {
			return
		}
	}
	t.Fatal("Read did not return the cookie from the browser-scoped CDP cookie store")
}

func TestConvertCookiePreservesCDPFields(t *testing.T) {
	in := &network.Cookie{
		Domain:       ".example.com",
		Name:         "session",
		Value:        "value",
		Path:         "/account",
		Expires:      42,
		Secure:       true,
		HTTPOnly:     true,
		Priority:     network.CookiePriorityHigh,
		SameSite:     network.CookieSameSiteStrict,
		SourceScheme: network.CookieSourceSchemeSecure,
		SourcePort:   443,
		Session:      false,
	}

	got := convertCookie(in)
	if got.HostKey != ".example.com" || got.Name != "session" || got.Value != "value" || got.Path != "/account" {
		t.Fatalf("identity fields = %#v", got)
	}
	if got.IsSecure != 1 || got.IsHTTPOnly != 1 || got.Priority != 2 || got.SameSite != 2 || got.SourceScheme != 2 || got.SourcePort != 443 {
		t.Fatalf("cookie attributes = %#v", got)
	}
	if got.ExpiresUTC == 0 || got.HasExpires != 1 || got.IsPersistent != 1 {
		t.Fatalf("expiry fields = %#v", got)
	}
}

func TestConvertCookieKeepsSessionCookieNonPersistent(t *testing.T) {
	got := convertCookie(&network.Cookie{Domain: "example.com", Name: "session", Value: "v", Path: "/", Session: true})
	if got.ExpiresUTC != 0 || got.HasExpires != 0 || got.IsPersistent != 0 {
		t.Fatalf("session cookie fields = %#v", got)
	}
}
