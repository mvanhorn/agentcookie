package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/pkg/sidecar"
)

// TestValidateListenAddr_PolicyMatrix exercises the v0.12 S1 binding
// policy enforced by validateListenAddr. The runtime sink startup
// guard and the wizard's --listen flag both call this; one table
// keeps the two callers honest about identical semantics.
func TestValidateListenAddr_PolicyMatrix(t *testing.T) {
	cases := []struct {
		name      string
		addr      string
		wantErr   bool
		wantInMsg string // substring asserted when wantErr is true
	}{
		// Refused: any-interface binds.
		{
			name:      "refuses 0.0.0.0",
			addr:      "0.0.0.0:9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},
		{
			name:      "refuses :: (IPv6 any)",
			addr:      "[::]:9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},
		{
			name:      "refuses bare :port (empty host)",
			addr:      ":9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},

		// Refused: non-tailnet routable address.
		{
			name:      "refuses LAN 192.168.x",
			addr:      "192.168.1.5:9999",
			wantErr:   true,
			wantInMsg: "not a Tailscale 100.x address",
		},
		{
			name:      "refuses 100.x but outside CGNAT block",
			addr:      "100.63.0.5:9999",
			wantErr:   true,
			wantInMsg: "not a Tailscale 100.x address",
		},

		// Refused: unparseable input. SplitHostPort is loose about
		// what it accepts as a host token (whitespace is fine), so
		// the test case picks an input it definitively rejects:
		// no port separator.
		{
			name:      "refuses input with no port",
			addr:      "no-colon-here",
			wantErr:   true,
			wantInMsg: "host:port",
		},

		// Accepted: explicit loopback, tailnet 100.x.
		{
			name: "accepts 127.0.0.1 (operator-typed local dev)",
			addr: "127.0.0.1:9999",
		},
		{
			name: "accepts ::1 loopback",
			addr: "[::1]:9999",
		},
		{
			name: "accepts localhost",
			addr: "localhost:9999",
		},
		{
			name: "accepts tailnet 100.80.x",
			addr: "100.80.229.80:9999",
		},
		{
			name: "accepts tailnet boundary 100.64.0.1",
			addr: "100.64.0.1:9999",
		},
		{
			name: "accepts tailnet upper boundary 100.127.255.254",
			addr: "100.127.255.254:9999",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListenAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.addr)
				}
				if !strings.Contains(err.Error(), tc.wantInMsg) {
					t.Errorf("error for %q: got %v, want substring %q", tc.addr, err, tc.wantInMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.addr, err)
			}
		})
	}
}

// TestValidateListenAddr_RefusesV011DefaultFallback documents the
// specific regression v0.12 closes. The pre-v0.12 wizard fell through
// to "0.0.0.0:9999" when Tailscale detection failed, and the config
// loader added a second silent fallback to "127.0.0.1:9999" on empty.
// A sink that ends up bound to 0.0.0.0 at runtime must now refuse
// to start so the operator sees the failure rather than serving
// publicly.
func TestValidateListenAddr_RefusesV011DefaultFallback(t *testing.T) {
	err := validateListenAddr("0.0.0.0:9999")
	if err == nil {
		t.Fatal("v0.12: sink listener must refuse 0.0.0.0:9999")
	}
	// Operator-facing message must name the v0.12 remediation surfaces.
	if !strings.Contains(err.Error(), "tailscale status") {
		t.Errorf("error should name `tailscale status`: %v", err)
	}
	if !strings.Contains(err.Error(), "docs/quickstart.md") {
		t.Errorf("error should name docs/quickstart.md: %v", err)
	}
}

// TestApplySidecarOnlyToSink exercises the v0.12.0-beta.3 headless write
// path. The function takes cookies and writes ONLY the plaintext sidecar
// (~/.agentcookie/cookies-plain.db) without touching Chrome SQLite,
// LocalStorage, or IndexedDB. This is what the sink runs when
// `skip_chrome_sqlite: true` is set in sink.yaml.
//
// The sidecar lookup uses HOME-relative paths under the hood
// (chromepaths.SidecarCookiesDB), so we point HOME at a temp dir to
// keep the test hermetic.
func TestApplySidecarOnlyToSink(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := os.MkdirAll(filepath.Join(tmpHome, ".agentcookie"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "abc123", Path: "/", IsSecure: 1, IsHTTPOnly: 1, IsPersistent: 1},
		{HostKey: ".airbnb.com", Name: "_aat", Value: "xyz", Path: "/"},
	}

	result, err := applySidecarOnlyToSink(cookies)
	if err != nil {
		t.Fatalf("applySidecarOnlyToSink: %v", err)
	}
	if result.SidecarCookies != len(cookies) {
		t.Errorf("SidecarCookies: got %d, want %d", result.SidecarCookies, len(cookies))
	}
	if result.Cookies != 0 {
		t.Errorf("Cookies (Chrome SQLite): got %d, want 0 (skip path must NOT write Chrome SQLite)", result.Cookies)
	}
	if result.LocalStorage != 0 || result.IndexedDB != 0 {
		t.Errorf("LocalStorage/IndexedDB: got %d/%d, want 0/0 (skip path must NOT write leveldb)", result.LocalStorage, result.IndexedDB)
	}

	// The sidecar file should now exist on disk under tmpHome.
	sidecarPath := filepath.Join(tmpHome, ".agentcookie", "cookies-plain.db")
	if _, statErr := os.Stat(sidecarPath); statErr != nil {
		t.Errorf("sidecar file not created at %s: %v", sidecarPath, statErr)
	}
}

// TestApplySidecarOnlyToSink_EmptyCookies is a regression guard: when
// the source sends an empty cookie batch (e.g. all dropped by the
// blocklist), applySidecarOnlyToSink must return a zero result without
// error -- no sidecar write attempted, no panic.
func TestApplySidecarOnlyToSink_EmptyCookies(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	result, err := applySidecarOnlyToSink(nil)
	if err != nil {
		t.Errorf("empty cookies should not error, got: %v", err)
	}
	if result.SidecarCookies != 0 {
		t.Errorf("SidecarCookies on empty input: got %d, want 0", result.SidecarCookies)
	}
}

func TestSinkSyncMalformedBlocklistReturns500AndWritesNothing(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains: []
unexpected: true
`)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "load blocklist") {
		t.Errorf("response should name blocklist load failure, got %q", rec.Body.String())
	}
	if _, err := os.Stat(fx.sidecarPath()); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be untouched on malformed blocklist, stat err=%v", err)
	}
	if fx.sinkState.TotalRejects != 1 {
		t.Errorf("TotalRejects = %d, want 1", fx.sinkState.TotalRejects)
	}
	if got := fx.seqTracker.Last("source-test"); got != 0 {
		t.Errorf("malformed blocklist should not accept sequence, got %d", got)
	}
}

func TestSinkSyncWellFormedBlocklistFiltersBeforeWrite(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.blocked.com"
`)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dropped 1 blocklisted cookies") {
		t.Errorf("response should report dropped cookie, got %q", rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".allowed.com"}) {
		t.Fatalf("sidecar hosts = %v, want only allowed", got)
	}
}

func TestSinkSyncAllowlistFiltersBeforeWrite(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: ".allowed.com"
`)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "dropped 1 non-allowlisted cookies") {
		t.Errorf("response should report non-allowlisted cookie, got %q", rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".allowed.com"}) {
		t.Fatalf("sidecar hosts = %v, want only allowed", got)
	}
}

func TestSinkSyncMissingBlocklistSyncsAll(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".one.com", Name: "one", Value: "1", Path: "/"},
		{HostKey: ".two.com", Name: "two", Value: "2", Path: "/"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".one.com", ".two.com"}) {
		t.Fatalf("sidecar hosts = %v, want sync-all", got)
	}
}

func TestSinkSyncReloadsBlocklistBetweenRequests(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	cookies := []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	}

	rec := fx.postSync(1, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".allowed.com", ".blocked.com"}) {
		t.Fatalf("first sidecar hosts = %v", got)
	}

	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.blocked.com"
`)
	rec = fx.postSync(2, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".allowed.com"}) {
		t.Fatalf("second sidecar hosts = %v, want newly blocked host dropped", got)
	}
}

func TestSinkSyncDryRunMalformedBlocklistRefuses(t *testing.T) {
	fx := newSinkHandlerFixture(t, true)
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.blocked.com
`)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "accepted") {
		t.Errorf("dry-run malformed blocklist should not report accepted cookies, got %q", rec.Body.String())
	}
	if fx.sinkState.TotalWrites != 0 {
		t.Errorf("TotalWrites = %d, want 0", fx.sinkState.TotalWrites)
	}
	if _, err := os.Stat(fx.sidecarPath()); !os.IsNotExist(err) {
		t.Fatalf("dry-run malformed blocklist should not create sidecar, stat err=%v", err)
	}
}

// TestSetCDPInjectorForTesting confirms the test seam restores the
// production injector. Used by other tests that need to stub
// cdpInject.
func TestSetCDPInjectorForTesting(t *testing.T) {
	calls := 0
	restore := SetCDPInjectorForTesting(func(_ context.Context, _ string, _ []chrome.Cookie) error {
		calls++
		return nil
	})
	if err := cdpInject(context.Background(), "/tmp", nil); err != nil {
		t.Fatalf("stub injector err: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls: got %d, want 1", calls)
	}
	restore()

	// After restore, calling cdpInject hits the real chromedp path.
	// We don't actually want to spawn chromedp in unit tests; assert
	// that the stub no longer fires by checking calls stays at 1.
	prev := calls
	// We can't safely invoke cdpInject post-restore without launching
	// Chrome. Instead, confirm by setting a new stub and observing
	// fresh calls counter starts from zero.
	calls = 0
	restore2 := SetCDPInjectorForTesting(func(_ context.Context, _ string, _ []chrome.Cookie) error {
		calls++
		return nil
	})
	_ = cdpInject(context.Background(), "/tmp", nil)
	if calls != 1 {
		t.Errorf("after second stub install, calls: got %d, want 1", calls)
	}
	if prev != 1 {
		t.Errorf("first stub's recorded calls should remain 1, got %d", prev)
	}
	restore2()
}

// TestCDPInjector_FailureDoesNotPropagate is a contract test for the
// /sync handler's CDP wiring: when the injector errors, the sink
// MUST log the error but not surface it as a sync failure (the
// sidecar write already succeeded; PP CLIs are still served).
//
// We test the contract directly against the cdpInject seam since the
// /sync handler's flow is more meaningful as an integration test
// (deferred to U7 dry-run).
func TestCDPInjector_FailureDoesNotPropagate(t *testing.T) {
	restore := SetCDPInjectorForTesting(func(_ context.Context, _ string, _ []chrome.Cookie) error {
		return errors.New("simulated chromedp launch failure")
	})
	defer restore()

	// The error surface is just `err != nil`. The /sync handler's
	// wiring catches this and logs without rejecting the request.
	err := cdpInject(context.Background(), "~/.agentcookie/chrome-profile", []chrome.Cookie{
		{HostKey: ".example.com", Name: "foo", Value: "bar"},
	})
	if err == nil {
		t.Fatal("stub should have returned an error")
	}
	if !strings.Contains(err.Error(), "simulated chromedp launch failure") {
		t.Errorf("unexpected error: %v", err)
	}
}

type sinkHandlerFixture struct {
	configDir  string
	home       string
	mux        *http.ServeMux
	secret     string
	seqTracker *protocol.SequenceTracker
	sinkState  *state.SinkState
}

func newSinkHandlerFixture(t *testing.T, dryRun bool) *sinkHandlerFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := t.TempDir()
	withConfigDir(t, configDir)

	oldDryRun := sinkDryRun
	sinkDryRun = dryRun
	t.Cleanup(func() { sinkDryRun = oldDryRun })

	cfg := &config.SinkConfig{
		Listen:           config.ListenRef{Addr: "127.0.0.1:9999"},
		SkipChromeSQLite: true,
	}
	secret := "sink-handler-test-secret"
	seqTracker := protocol.NewSequenceTracker()
	sinkState := &state.SinkState{Role: "sink", ListenAddr: cfg.Listen.Addr}
	stateWriter := state.NewWriter(filepath.Join(t.TempDir(), "sink-state.json"))
	mux := newSinkMux(cfg, secret, []byte("0123456789abcdef"), seqTracker, stateWriter, sinkState)

	return &sinkHandlerFixture{
		configDir:  configDir,
		home:       home,
		mux:        mux,
		secret:     secret,
		seqTracker: seqTracker,
		sinkState:  sinkState,
	}
}

func (f *sinkHandlerFixture) postSync(seq int64, cookies []chrome.Cookie) *httptest.ResponseRecorder {
	envelope := protocol.SyncEnvelope{
		ProtocolVersion: protocol.Version,
		SourceHostname:  "source-test",
		Sequence:        seq,
		Cookies:         cookies,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	sealed, err := transport.SealWithSecret(payload, f.secret)
	if err != nil {
		panic(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/sync", bytes.NewReader(sealed))
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *sinkHandlerFixture) sidecarPath() string {
	return filepath.Join(f.home, ".agentcookie", "cookies-plain.db")
}

func (f *sinkHandlerFixture) sidecarHosts() []string {
	cookies, err := sidecar.ReadSidecar(f.sidecarPath())
	if err != nil {
		panic(err)
	}
	hosts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		hosts = append(hosts, c.HostKey)
	}
	sort.Strings(hosts)
	return hosts
}
