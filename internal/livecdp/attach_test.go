package livecdp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestShouldInjectTarget(t *testing.T) {
	cases := []struct {
		name string
		info *target.Info
		want bool
	}{
		{"normal page", &target.Info{Type: "page", URL: "https://github.com"}, true},
		{"about:blank page", &target.Info{Type: "page", URL: "about:blank"}, true},
		{"chrome:// page", &target.Info{Type: "page", URL: "chrome://newtab"}, false},
		{"devtools page", &target.Info{Type: "page", URL: "devtools://devtools/bundled"}, false},
		{"extension page", &target.Info{Type: "page", URL: "chrome-extension://abc/popup.html"}, false},
		{"prerender subtype", &target.Info{Type: "page", Subtype: "prerender", URL: "https://x.com"}, false},
		{"service worker", &target.Info{Type: "service_worker", URL: "https://x.com/sw.js"}, false},
		{"background page", &target.Info{Type: "background_page", URL: "chrome-extension://abc"}, false},
		{"browser target", &target.Info{Type: "browser"}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldInjectTarget(tc.info); got != tc.want {
				t.Errorf("shouldInjectTarget(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestInjectAllContexts_LiveLogin exercises the enumerate-contexts +
// dedup-by-BrowserContextID + per-target inject path end to end against a
// live Chrome: after InjectAllContexts, the page sends the injected cookie.
// True isolated-context coverage (browser-use's own context) is proven by
// the real browser-use integration in the agent-sync flow, not synthesized
// here -- synthetic browser-context creation fights chromedp's lazy target
// model and would test the harness, not this code. Gated behind
// AGENTCOOKIE_LIVE_CDP_TEST (launches real Chrome).
func TestInjectAllContexts_LiveLogin(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 to run the live InjectAllContexts test")
	}

	var mu sync.Mutex
	var saw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("ac_all_test"); err == nil {
			mu.Lock()
			saw = ck.Value
			mu.Unlock()
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", "new"),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
		)...)
	defer allocCancel()
	browserCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	browserCtx, tcancel := context.WithTimeout(browserCtx, 40*time.Second)
	defer tcancel()

	if err := chromedp.Run(browserCtx, chromedp.Navigate("about:blank")); err != nil {
		t.Fatalf("start browser: %v", err)
	}

	cookie := chrome.Cookie{HostKey: u.Hostname(), Name: "ac_all_test", Value: "all-ctx", Path: "/", IsSecure: 0, SameSite: 1}
	n, err := InjectAllContexts(browserCtx, []chrome.Cookie{cookie})
	if err != nil {
		t.Fatalf("InjectAllContexts: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected injection into >=1 context, got %d", n)
	}

	if err := chromedp.Run(browserCtx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if saw != "all-ctx" {
		t.Fatalf("page did not send injected cookie; got %q", saw)
	}
}
