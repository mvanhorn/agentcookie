package storagecodec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// This required gate NEVER converts browser startup failures into a skip.
// Enable on a runner permitting browser processes and AF_UNIX sockets.
func TestHelperBrowserServiceWorkerAndHostileTab(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_BROWSER_TEST") != "1" {
		t.Skip("set AGENTCOOKIE_BROWSER_TEST=1 on a browser-capable fixture runner")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	for _, dir := range []string{filepath.Join(home, "config"), filepath.Join(home, "tmp")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`self.addEventListener('install',e=>e.waitUntil(self.skipWaiting()));self.addEventListener('activate',e=>e.waitUntil(clients.claim()));self.addEventListener('fetch',e=>{if(e.request.url.includes('/.agentcookie-apply/'))e.respondWith(new Response('<title>hostile service worker</title>'));});`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>fixture</title><script>window.events=[];addEventListener('storage',e=>{events.push(e.key);fetch('/storage-beacon')});</script>`))
	}))
	defer server.Close()
	executable := os.Getenv("AGENTCOOKIE_CHROME_BINARY")
	if executable == "" {
		executable = os.Getenv("CHROME_BIN")
	}
	if executable == "" {
		executable = "/usr/bin/chromium"
	}
	options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(executable), chromedp.UserDataDir(filepath.Join(home, "profile")), chromedp.NoSandbox, chromedp.Flag("disable-background-networking", true), chromedp.Flag("use-mock-keychain", true), chromedp.Flag("password-store", "basic"), chromedp.Flag("remote-debugging-address", "127.0.0.1"), chromedp.Flag("disable-breakpad", true), chromedp.Flag("disable-crash-reporter", true))
	alloc, cancel := chromedp.NewExecAllocator(context.Background(), options...)
	defer cancel()
	browser, closeBrowser := chromedp.NewContext(alloc)
	defer closeBrowser()
	ctx, stop := context.WithTimeout(browser, 30*time.Second)
	defer stop()
	if err := chromedp.Run(ctx, chromedp.Navigate(server.URL+"/seed"), chromedp.Evaluate(`(async()=>{await navigator.serviceWorker.register('/sw.js');await navigator.serviceWorker.ready;if(!navigator.serviceWorker.controller)await new Promise(r=>navigator.serviceWorker.addEventListener('controllerchange',r,{once:true}));return true})()`, nil, chromedp.EvalAsValue)); err != nil {
		t.Fatal(err)
	}
	// Await controller separately: Evaluate's promise behavior differs by options.
	if err := chromedp.Run(ctx, chromedp.Poll(`!!navigator.serviceWorker.controller`, nil, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatal(err)
	}
	before, err := chromedp.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	helper := &Helper{Browser: ctx}
	args := map[string]any{"op": "set", "key": str("fixture-token"), "value": str("fixture-value")}
	if err := helper.Run(ctx, "", server.URL, "localStorage", args, nil); err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Absent bool `json:"absent"`
	}
	if err := helper.Run(ctx, "", server.URL, "readLocalStorage", map[string]any{"key": str("fixture-token")}, &observed); err != nil || observed.Absent {
		t.Fatal(observed, err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(`events.includes('fixture-token')`, nil, chromedp.WithPollingTimeout(3*time.Second))); err != nil {
		t.Fatal("hostile open tab did not receive documented storage event", err)
	}
	after, err := chromedp.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("helper target leaked", len(before), len(after))
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if strings.Contains(p, "/.agentcookie-apply/") {
			t.Fatalf("helper request reached server: %s", p)
		}
	}
}
