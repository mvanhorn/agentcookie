package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/cli"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

func TestForbiddenProvidersPanicBeforeHostAccess(t *testing.T) {
	_ = New(t)
	for name, call := range map[string]func(){"keychain": func() { _, _ = chrome.SafeStoragePasswordFor(chrome.Browser{}) }, "master": func() { _, _ = keystore.ReadMasterKey() }, "discovery": func() { _, _ = secretsbus.LoadPayloadWithDiscovery("/must-not-read") }} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("forbidden provider did not panic")
				}
			}()
			call()
		})
	}
}

func TestChromiumReaderWireToBrowser(t *testing.T) {
	h := New(t)
	ctx := h.Browser()
	cookies := []chrome.Cookie{
		{HostKey: "fixture.localhost", Name: "host", Value: "host-value", Path: "/", SameSite: -1},
		{HostKey: ".fixture.localhost", Name: "domain", Value: "domain-value", Path: "/", SameSite: -1},
		{HostKey: "fixture.localhost", Name: "__Host-auth", Value: "host-prefix", Path: "/", IsSecure: 1, SameSite: -1},
		{HostKey: "fixture.localhost", Name: "persistent", Value: "expiry", Path: "/", HasExpires: 1, IsPersistent: 1, ExpiresUTC: (time.Now().Add(time.Hour).Unix() + 11644473600) * 1000000, SameSite: -1},
	}
	cookies = append(cookies,
		chrome.Cookie{HostKey: "fixture.localhost", Name: "chipsSame", Value: "same-site", Path: "/", IsSecure: 1, SameSite: 0, TopFrameSiteKey: "http://fixture.localhost", HasCrossSiteAncestor: 0},
		chrome.Cookie{HostKey: "fixture.localhost", Name: "chipsCross", Value: "cross-site", Path: "/", IsSecure: 1, SameSite: 0, TopFrameSiteKey: "http://other.localhost", HasCrossSiteAncestor: 1})
	cfg := h.ChromiumFixture(cookies)
	snapshot, err := h.Reader(cfg).Read(context.Background(), &config.Blocklist{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stats.TotalRead != len(cookies) {
		t.Fatalf("read %d cookies", snapshot.Stats.TotalRead)
	}
	// Actual CLI source and sink assembled in process, with fixture password,
	// masterKey:nil, adapters:none, discovery:none, --skip-secrets and --json.
	secret := "fixture-transport-secret"
	handler, push, restore, err := cli.HermeticPipelineForTesting(filepath.Join(h.Home, "config"), secret, func(chrome.Browser) (string, error) { return FixturePassword, nil }, func(_ context.Context, cookies []chrome.Cookie) error { return livecdp.Inject(ctx, cookies) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync" {
			handler.ServeHTTP(w, r)
			return
		}
		wire, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		plain, err := transport.OpenWithSecret(wire, secret)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var env protocol.SyncEnvelope
		if err = json.Unmarshal(plain, &env); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(env.Secrets) != 0 {
			t.Error("hermetic source read secrets")
		}
		r.Body = io.NopCloser(bytes.NewReader(wire))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(sink.Close)
	cfg.Sink = config.SinkRef{URL: sink.URL + "/sync"}
	cfg.Security.SharedSecret = secret
	if err := push(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		_, _ = io.WriteString(w, r.Header.Get("Cookie"))
	}))
	t.Cleanup(site.Close)
	parsed, _ := url.Parse(site.URL)
	for _, host := range []string{"fixture.localhost", "sub.fixture.localhost"} {
		var got string
		if err := chromedp.Run(ctx, chromedp.Navigate("http://"+host+":"+parsed.Port()), chromedp.Text("body", &got)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "domain=domain-value") {
			t.Fatalf("%s missing domain cookie: %s", host, got)
		}
		if host == "fixture.localhost" && !strings.Contains(got, "host=host-value") {
			t.Fatalf("host-only missing: %s", got)
		}
		if host != "fixture.localhost" && (strings.Contains(got, "host=host-value") || strings.Contains(got, "__Host-auth=")) {
			t.Fatalf("host-only widened: %s", got)
		}
	}
	// Read the wire-delivered CHIPS cookies in matching and unrelated top-level
	// contexts, covering both legitimate ancestor bit values.
	for _, scope := range []struct{ top, want, absent string }{{"fixture.localhost", "chipsSame=same-site", "chipsCross="}, {"other.localhost", "chipsCross=cross-site", "chipsSame="}, {"unrelated.localhost", "", "chips"}} {
		var got string
		endpoint := "http://fixture.localhost:" + parsed.Port() + "/echo"
		expression := `fetch(` + strconv.Quote(endpoint) + `,{credentials:"include"}).then(r=>r.text())`
		if err := chromedp.Run(ctx, chromedp.Navigate("http://"+scope.top+":"+parsed.Port()), chromedp.Evaluate(expression, &got, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) })); err != nil {
			t.Fatal(err)
		}
		if (scope.want != "" && !strings.Contains(got, scope.want)) || strings.Contains(got, scope.absent) {
			t.Fatalf("partition context %s: got %q, want %q absent %q", scope.top, got, scope.want, scope.absent)
		}
	}
	var jar []*network.Cookie
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		jar, err = network.GetCookies().WithURLs([]string{"http://fixture.localhost/"}).Do(ctx)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	seen := map[string]*network.Cookie{}
	for _, cookie := range jar {
		seen[cookie.Name] = cookie
	}
	if seen["host"] == nil || !seen["host"].Session {
		t.Fatal("session expiry was made persistent")
	}
	if seen["host"].SameSite != "" {
		t.Fatalf("unspecified SameSite was rewritten: %q", seen["host"].SameSite)
	}
	if seen["persistent"] == nil || seen["persistent"].Session {
		t.Fatal("finite expiry was dropped")
	}
	if seen["__Host-auth"] == nil || !seen["__Host-auth"].Secure {
		t.Fatal("__Host- secure cookie missing")
	}
}

// This full source/wire/sink test runs even when Chromium cannot start. The
// browser applier is injected, but real fixture SQLite, decryption, policy,
// encrypted transport, replay tracking and sidecar writes are exercised.
func TestHermeticChromiumPipeline(t *testing.T) {
	h := New(t)
	cfg := h.ChromiumFixture([]chrome.Cookie{{HostKey: "fixture.localhost", Name: "auth", Value: "synthetic", Path: "/", SameSite: -1}})
	applied := make(chan []chrome.Cookie, 1)
	const secret = "fixture-transport-secret"
	handler, push, restore, err := cli.HermeticPipelineForTesting(filepath.Join(h.Home, "config"), secret, func(chrome.Browser) (string, error) { return FixturePassword, nil }, func(_ context.Context, cookies []chrome.Cookie) error { applied <- cookies; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	sink := httptest.NewServer(handler)
	t.Cleanup(sink.Close)
	cfg.Sink = config.SinkRef{URL: sink.URL + "/sync"}
	cfg.Security.SharedSecret = secret
	if err := push(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-applied:
		if len(got) != 1 || got[0].Value != "synthetic" {
			t.Fatalf("source/sink lost cookie: %#v", got)
		}
	default:
		t.Fatal("source/sink did not apply cookie")
	}
}
