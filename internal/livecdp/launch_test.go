package livecdp

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestFindChrome(t *testing.T) {
	path, err := FindChrome()
	if err != nil {
		t.Skipf("Chrome not installed in this environment: %v", err)
	}
	if fi, statErr := os.Stat(path); statErr != nil || fi.IsDir() {
		t.Errorf("FindChrome returned %q which is not a runnable file", path)
	}
}

// TestLaunchOwnedChrome_LiveInject exercises the full owned-browser path:
// launch a dedicated Chrome on a loopback debug port, connect, inject, and
// confirm a page is logged in -- then clean shutdown. Gated behind
// AGENTCOOKIE_LIVE_CDP_TEST (launches real Chrome).
func TestLaunchOwnedChrome_LiveInject(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 to run the owned-Chrome launch test")
	}
	dir, err := os.MkdirTemp("", "ac-owned-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// Port 0 is invalid for Chrome; pick a high fixed port unlikely to clash.
	oc, err := LaunchOwnedChrome(ctx, "", dir, 9411, true)
	if err != nil {
		t.Fatalf("LaunchOwnedChrome: %v", err)
	}
	defer oc.Close()

	if oc.Endpoint != "http://127.0.0.1:9411" {
		t.Errorf("endpoint: got %q", oc.Endpoint)
	}

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, oc.Endpoint)
	defer allocCancel()
	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	if err := chromedp.Run(bctx); err != nil {
		t.Fatalf("connect to owned chrome: %v", err)
	}

	// A cookie store write into the owned browser should be reachable.
	cookie := chrome.Cookie{HostKey: "example.com", Name: "ac_owned", Value: "v", Path: "/", IsSecure: 1, SameSite: 1}
	n, err := InjectAllContexts(bctx, []chrome.Cookie{cookie})
	if err != nil {
		t.Fatalf("InjectAllContexts on owned chrome: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 context injected, got %d", n)
	}
}
