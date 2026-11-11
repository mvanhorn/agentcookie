//go:build agentcookie_cookie_e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/e2e"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// This PR4-only gate uses the actual Gecko live reader, negotiated encrypted
// sender, sink HTTP handler and owned Chrome cookie applier. Its build tag is
// listed/run explicitly by Linux and pinned macOS CI after PR3 prerequisites.
// No real browser profile, credential provider, adapter or discovery is used.
func TestGeckoLiveCookieSourceSinkOwnedChrome(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_COOKIE_E2E") != "1" {
		t.Skip("explicit cookie pipeline browser gate not requested")
	}
	for _, name := range []string{"AGENTCOOKIE_GECKO_BINARY", "AGENTCOOKIE_CHROME_BINARY", "AGENTCOOKIE_BINARY"} {
		if os.Getenv(name) == "" {
			t.Fatalf("required fixture binary environment missing: %s", name)
		}
	}
	g := e2e.NewGecko(t) // Includes a real-provider guard and throwaway HOME.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Cleanup(sinkpush.SetRegistryForTesting(nil))
	var phase atomic.Int64
	phase.Store(1)
	reported := make(chan string, 8)
	requests := make(chan map[string]string, 8)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/phase":
			fmt.Fprint(w, phase.Load())
		case "/report":
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			select {
			case reported <- string(body):
			default:
			}
		case "/verify":
			cookies := map[string]string{}
			for _, c := range r.Cookies() {
				cookies[c.Name] = c.Value
			}
			select {
			case requests <- cookies:
			default:
			}
			fmt.Fprint(w, "<!doctype html><title>cookie fixture</title>")
		default:
			fmt.Fprint(w, "<!doctype html><title>fixture</title>")
		}
	}))
	defer site.Close()
	script := `(async()=>{const origin=` + strconv.Quote(site.URL) + `;let last=0,busy=false;
 async function step(){if(busy)return;busy=true;try{const next=Number(await(await fetch(origin+'/phase')).text());if(next!==last){if(next===3)await browser.cookies.remove({url:origin+'/',name:'session',storeId:'firefox-default'});else await browser.cookies.set({url:origin+'/',name:'session',value:next===1?'token-one':'token-two',path:'/',expirationDate:Date.now()/1000+3600,storeId:'firefox-default'});last=next;await fetch(origin+'/report',{method:'POST',body:'phase:'+next})}}catch(e){await fetch(origin+'/report',{method:'POST',body:'error:'+e})}finally{busy=false}}
 await step();setInterval(step,200)})();`
	g.InstallFixtureExtension(script)
	await := func(want string) {
		t.Helper()
		select {
		case got := <-reported:
			if got != want {
				t.Fatalf("Gecko fixture %q want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("Gecko fixture mutation timed out")
		}
	}
	await("phase:1")
	opts := g.ReaderOptions()
	opts.Containers = []string{"default"}
	reader, err := geckoreader.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	dir := filepath.Join(g.Home, "live-cookie-wire")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	withConfigDir(t, dir)
	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains: []\n")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	headed := false
	cfg := config.DestinationRef{Name: "live", Kind: "live_chrome", Required: true, Chrome: config.DestinationChrome{UserDataDir: filepath.Join(g.Home, "owned-chrome"), Port: port, Path: os.Getenv("AGENTCOOKIE_CHROME_BINARY"), Headed: &headed, Start: "on_demand", Relaunch: "after_crash"}}
	managed := destination.NewManaged(cfg)
	managed.MockCredentials = true
	defer managed.Close()
	engine, err := destination.Open(filepath.Join(dir, "destination"), nil, managed)
	if err != nil {
		t.Fatal(err)
	}
	pipeline := &destinationPipeline{order: []string{"live"}, entries: map[string]*destinationEntry{"live": {config: cfg, engine: engine, managed: managed, surfaces: []string{"cookies"}}}}
	previousConverter := convertDestinationSnapshot
	convertDestinationSnapshot = destination.CookieSnapshot
	defer func() { convertDestinationSnapshot = previousConverter }()
	mux := newSinkMux(&config.SinkConfig{}, "fixture-key", nil, protocol.NewSequenceTracker(), state.NewWriter(filepath.Join(dir, "sink-state.json")), &state.SinkState{}, &sync.Mutex{}, nil, pipeline)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "read", 400)
				return
			}
			if bytes.Contains(body, []byte("token-one")) || bytes.Contains(body, []byte("token-two")) {
				t.Error("source sent plaintext cookies")
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		mux.ServeHTTP(w, r)
	}))
	defer sink.Close()
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, u string) (string, error) { return u, nil }))
	counters := source.Counters{Dir: filepath.Join(dir, "source")}
	send := func(want string) protocol.SyncEnvelope {
		t.Helper()
		policy, err := loadFreshBlocklist()
		if err != nil {
			t.Fatal(err)
		}
		capture, err := reader.Read(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		if capture.Meta.Surfaces[protocol.Cookies].State != protocol.Complete {
			t.Fatal("live cookie read is not complete")
		}
		actual := ""
		for _, c := range capture.Cookies {
			if c.Name == "session" {
				actual = c.Value
			}
		}
		if actual != want {
			t.Fatalf("live source cookie %q want %q", actual, want)
		}
		matcher := protocol.NewBlocklistMatcher(policy)
		for _, cookie := range capture.Cookies {
			if !matcher.ShouldSyncHost(cookie.HostKey) {
				t.Fatal("source emitted a cookie excluded by the current accounts policy")
			}
		}
		generation, err := counters.NextGeneration()
		if err != nil {
			t.Fatal(err)
		}
		meta := capture.Meta
		meta.Generation = generation
		meta.Surfaces = map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}}
		env := protocol.SyncEnvelope{SourceHostname: "cookie-fixture", SourceBrowser: "firefox", ReaderMode: capture.ReaderMode, Cookies: capture.Cookies, Snapshot: &meta}
		body, version, err := sendNegotiated(ctx, config.SinkTarget{URL: sink.URL + "/sync", Destination: "live"}, "fixture-key", env, counters, false)
		if err != nil || version != 3 {
			t.Fatalf("negotiated send v%d: %v", version, err)
		}
		var reply syncReply
		if err = json.Unmarshal([]byte(body), &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.Destinations) != 1 || !reply.Destinations[0].Ready || reply.Destinations[0].Accepted != generation {
			t.Fatalf("not applied: %s", body)
		}
		status, err := getSinkStatus(ctx, sink.URL, "fixture-key", "cookie-fixture")
		if err != nil || status.Generation != generation {
			t.Fatalf("authenticated status: %+v %v", status, err)
		}
		return env
	}
	verify := func(want string, keep bool) {
		t.Helper()
		ws, err := livecdp.VerifiedOwnedChromeWebSocket(ctx, cfg.Chrome.UserDataDir, port)
		if err != nil {
			t.Fatal(err)
		}
		probe, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		browser, err := chromedp.NewBrowser(probe, ws)
		if err != nil {
			t.Fatal(err)
		}
		exec := cdp.WithExecutor(probe, browser)
		tabs, err := target.GetTargets().Do(exec)
		if err != nil {
			t.Fatal(err)
		}
		for _, tab := range tabs {
			if tab.Type == "page" {
				t.Fatal("visible destination page created before verified restore")
			}
		}
		id, err := target.CreateTarget(site.URL + "/verify").WithHidden(true).Do(exec)
		if err != nil {
			t.Fatal(err)
		}
		defer target.CloseTarget(id).Do(exec)
		select {
		case cookies := <-requests:
			if cookies["session"] != want {
				t.Fatalf("request cookie %q want %q", cookies["session"], want)
			}
			if keep && cookies["sink_only"] != "keep" {
				t.Fatal("unowned sink cookie lost")
			}
		case <-probe.Done():
			t.Fatal("Chrome fixture request timed out")
		}
	}
	send("token-one")
	verify("token-one", false)
	contexts, err := managed.Contexts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	local, _, err := destination.CookieSnapshot(protocol.SyncEnvelope{Cookies: []chrome.Cookie{{HostKey: "127.0.0.1", Name: "sink_only", Value: "keep", Path: "/", SameSite: -1, HasExpires: 1, IsPersistent: 1, ExpiresUTC: (time.Now().Add(time.Hour).Unix() + 11644473600) * 1000000}}})
	if err != nil || len(local.Items) != 1 {
		t.Fatalf("local fixture conversion: %v", err)
	}
	if err = managed.Set(ctx, contexts[0], local.Items[0]); err != nil {
		t.Fatal(err)
	}
	phase.Store(2)
	await("phase:2")
	queuedOlder := send("token-two")
	verify("token-two", true)

	// Scenario 5: use the actual accounts command implementation on the
	// fixture blocklist, not a manually edited policy or synthetic snapshot.
	if err = runAccountsToggle(commandWithOutput(&bytes.Buffer{}), "127.0.0.1", false); err != nil {
		t.Fatal(err)
	}
	send("")
	verify("", true)
	unfiltered, err := reader.Read(ctx, &config.Blocklist{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	stillLoggedIn := false
	for _, cookie := range unfiltered.Cookies {
		if cookie.Name == "session" && cookie.Value == "token-two" {
			stillLoggedIn = true
		}
	}
	if !stillLoggedIn {
		t.Fatal("accounts off unexpectedly removed the source browser's own login")
	}
	if err = runAccountsToggle(commandWithOutput(&bytes.Buffer{}), "127.0.0.1", true); err != nil {
		t.Fatal(err)
	}
	send("token-two")
	verify("token-two", true)

	phase.Store(3)
	await("phase:3")
	logout := send("")
	verify("", true)

	// A delayed pre-logout generation gets a fresh transport sequence from
	// the production sender. Reject it semantically, not merely as replayed
	// transport, and prove it cannot revive the HTTP-visible login.
	_, version, staleErr := sendNegotiated(ctx, config.SinkTarget{URL: sink.URL + "/sync", Destination: "live"}, "fixture-key", queuedOlder, counters, false)
	if staleErr == nil || version != 3 {
		t.Fatalf("queued older generation accepted: v%d err=%v", version, staleErr)
	}
	status, err := getSinkStatus(ctx, sink.URL, "fixture-key", "cookie-fixture")
	if err != nil || status.Generation != logout.Snapshot.Generation || len(status.Destinations) != 1 || !status.Destinations[0].Ready {
		t.Fatalf("stale generation changed logout state: %+v %v", status, err)
	}
	verify("", true)
}
