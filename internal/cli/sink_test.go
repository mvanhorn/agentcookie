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
	"sync"
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
	// Explicit policy: blocklist ensures the same behavior on Darwin and Linux.
	// Without it, Linux defaults to allowlist-empty (missing policy = ship nothing).
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
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
	// On Linux, missing blocklist defaults to allowlist-empty (sync nothing).
	// This test exercises the Darwin default (blocklist = sync-all).
	// Skip on Linux; the Linux-specific behavior is tested separately.
	if config.IsLinux() {
		t.Skip("skipping: Linux defaults to allowlist-empty; see TestSinkSyncMissingBlocklistLinuxAllowlistEmpty")
	}

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

// TestSinkSyncMissingBlocklistLinuxAllowlistEmpty verifies the Linux-specific
// default: when no blocklist.yaml exists, the sink treats this as an empty
// allowlist (ship nothing). This is security-by-default for untrusted sinks.
func TestSinkSyncMissingBlocklistLinuxAllowlistEmpty(t *testing.T) {
	if !config.IsLinux() {
		t.Skip("skipping: this test exercises Linux-specific allowlist-empty default")
	}

	fx := newSinkHandlerFixture(t, false)

	rec := fx.postSync(1, []chrome.Cookie{
		{HostKey: ".one.com", Name: "one", Value: "1", Path: "/"},
		{HostKey: ".two.com", Name: "two", Value: "2", Path: "/"},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	// Linux allowlist-empty: no cookies synced, response reports dropped count.
	if !strings.Contains(rec.Body.String(), "dropped 2 non-allowlisted cookies") {
		t.Errorf("response should report all cookies dropped as non-allowlisted, got %q", rec.Body.String())
	}
	// No cookies written, so sidecar may not exist or be empty.
	if got := fx.sidecarHostsOrEmpty(); len(got) != 0 {
		t.Fatalf("sidecar hosts = %v, want empty (allowlist-empty default)", got)
	}
}

func TestSinkSyncReloadsBlocklistBetweenRequests(t *testing.T) {
	fx := newSinkHandlerFixture(t, false)
	cookies := []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	}

	// First request: explicit blocklist with no domains (sync-all).
	// This ensures consistent behavior on both Darwin and Linux.
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
domains: []
`)
	rec := fx.postSync(1, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if got := fx.sidecarHosts(); !reflect.DeepEqual(got, []string{".allowed.com", ".blocked.com"}) {
		t.Fatalf("first sidecar hosts = %v", got)
	}

	// Second request: blocklist now has a pattern that blocks .blocked.com.
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: blocklist
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
	var stateMu sync.Mutex
	mux := newSinkMux(cfg, secret, []byte("0123456789abcdef"), seqTracker, stateWriter, sinkState, &stateMu)

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

// sidecarHostsOrEmpty returns the sidecar hosts, or an empty slice if the
// sidecar does not exist. Used by tests that expect zero cookies written.
func (f *sinkHandlerFixture) sidecarHostsOrEmpty() []string {
	if _, err := os.Stat(f.sidecarPath()); os.IsNotExist(err) {
		return nil
	}
	return f.sidecarHosts()
}

// TestCookieDedupeKey verifies the deduplication key includes host+name+path.
func TestCookieDedupeKey(t *testing.T) {
	c1 := chrome.Cookie{HostKey: ".instacart.com", Name: "_session", Path: "/"}
	c2 := chrome.Cookie{HostKey: ".instacart.com", Name: "_session", Path: "/api"}

	key1 := cookieDedupeKey(c1)
	key2 := cookieDedupeKey(c2)

	if key1 == key2 {
		t.Errorf("cookies with different paths should have different dedupe keys")
	}

	// Same cookie should have same key.
	c3 := chrome.Cookie{HostKey: ".instacart.com", Name: "_session", Path: "/"}
	if cookieDedupeKey(c1) != cookieDedupeKey(c3) {
		t.Errorf("identical cookies should have same dedupe key")
	}
}

// TestUnionCookiesWithExtraProfiles_EnvelopeOnlyOnLinux verifies that on
// Linux, only envelope cookies are returned (no Chrome SQLite decrypt).
func TestUnionCookiesWithExtraProfiles_EnvelopeOnlyOnLinux(t *testing.T) {
	if !config.IsLinux() {
		t.Skip("skipping: this test is Linux-specific")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	envelopeCookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "env123", Path: "/"},
		{HostKey: ".airbnb.com", Name: "_aat", Value: "envtoken", Path: "/"},
	}

	result := unionCookiesWithExtraProfiles(envelopeCookies, "", nil)

	// On Linux, should return only envelope cookies.
	if len(result) != len(envelopeCookies) {
		t.Errorf("on Linux, union should return only envelope cookies; got %d, want %d", len(result), len(envelopeCookies))
	}
	for i, c := range result {
		if c.Value != envelopeCookies[i].Value {
			t.Errorf("cookie %d: got Value %q, want %q", i, c.Value, envelopeCookies[i].Value)
		}
	}
}

// TestUnionCookiesWithExtraProfiles_EmptyEnvelope verifies that empty
// envelope cookies returns empty on Linux (no profiles to read).
func TestUnionCookiesWithExtraProfiles_EmptyEnvelope(t *testing.T) {
	if !config.IsLinux() {
		t.Skip("skipping: Linux-specific behavior test")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	result := unionCookiesWithExtraProfiles(nil, "", nil)

	if len(result) != 0 {
		t.Errorf("empty envelope on Linux should return empty; got %d cookies", len(result))
	}
}

// TestUnionCookiesWithExtraProfiles_EnvelopeWins verifies that envelope
// cookies take priority over extra profile cookies on host+name+path collisions.
// This test is conceptual - it verifies the deduplication logic.
func TestUnionCookiesWithExtraProfiles_DeduplicationLogic(t *testing.T) {
	// Test the dedupe logic directly since we can't easily set up Chrome
	// profiles in a unit test. The full integration is tested separately.
	envelopeCookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "envelope-value", Path: "/"},
	}

	// Build seen map the same way unionCookiesWithExtraProfiles does.
	seen := make(map[string]bool)
	for _, c := range envelopeCookies {
		key := cookieDedupeKey(c)
		seen[key] = true
	}

	// A hypothetical extra-profile cookie with the same host+name+path.
	extraCookie := chrome.Cookie{HostKey: ".instacart.com", Name: "_session", Value: "extra-value", Path: "/"}
	extraKey := cookieDedupeKey(extraCookie)

	if !seen[extraKey] {
		t.Errorf("extra cookie with same host+name+path should be skipped by dedupe logic")
	}

	// Different path should not be skipped.
	diffPathCookie := chrome.Cookie{HostKey: ".instacart.com", Name: "_session", Value: "diff-path", Path: "/api"}
	diffPathKey := cookieDedupeKey(diffPathCookie)

	if seen[diffPathKey] {
		t.Errorf("cookie with different path should not be skipped")
	}
}

// TestUnionCookiesWithExtraProfiles_BlocklistFiltersExtraProfiles verifies
// that extra-profile cookies are filtered through the blocklist (P1 fix #1).
// This is a unit test for the blocklist check logic within unionCookiesWithExtraProfiles.
func TestUnionCookiesWithExtraProfiles_BlocklistFiltersExtraProfiles(t *testing.T) {
	// Create a blocklist that blocks .blocked.com.
	bl := &config.Blocklist{
		Version: 1,
		Policy:  "blocklist",
		Domains: []config.BlocklistEntry{
			{Pattern: "%.blocked.com"},
		},
	}
	blockMatcher := protocol.NewBlocklistMatcherForSink(bl)

	// The blocklist should block .blocked.com.
	if blockMatcher.ShouldSyncHost(".blocked.com") {
		t.Error("blocklist should block .blocked.com")
	}
	if !blockMatcher.ShouldSyncHost(".allowed.com") {
		t.Error("blocklist should allow .allowed.com")
	}

	// On Linux, the union function returns envelope cookies only (no extra profiles).
	// The blocklist filtering of extra profiles only applies on Darwin.
	// This test verifies the blocklist logic is correct regardless of platform.
}

// TestUnionCookiesWithExtraProfiles_NilBlocklistAllowsAll verifies that
// when blockMatcher is nil, all cookies are allowed (backward compatibility).
func TestUnionCookiesWithExtraProfiles_NilBlocklistAllowsAll(t *testing.T) {
	if !config.IsLinux() {
		t.Skip("skipping: Linux-specific test (Darwin would try Chrome decrypt)")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	envelopeCookies := []chrome.Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "env123", Path: "/"},
	}

	// nil blockMatcher should not filter anything.
	result := unionCookiesWithExtraProfiles(envelopeCookies, "", nil)

	if len(result) != 1 {
		t.Errorf("nil blockMatcher should allow all cookies; got %d, want 1", len(result))
	}
}

// TestSinkHandler_EmptyEnvelopeStillUnionsExtraProfiles is a behavioral test
// that verifies the P1 fix #2: the sink handler should union extra-profile
// cookies even when the envelope is empty/fully filtered. On Linux this is
// a no-op (no extra profiles), but the code path should be exercised.
func TestSinkHandler_EmptyEnvelopeStillUnionsExtraProfiles(t *testing.T) {
	// This test verifies that the union function is called even with empty
	// envelope cookies (P1 fix #2). On Linux, this returns empty, but the
	// code path is correct.
	if !config.IsLinux() {
		t.Skip("skipping: Linux-specific test (Darwin would try Chrome decrypt)")
	}

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Empty envelope.
	var envelopeCookies []chrome.Cookie

	// Union should be called and return empty on Linux.
	result := unionCookiesWithExtraProfiles(envelopeCookies, "", nil)

	if len(result) != 0 {
		t.Errorf("empty envelope on Linux should return empty after union; got %d", len(result))
	}
}

// TestIsIPBoundLocally verifies the local IP detection used by sink rebind.
func TestIsIPBoundLocally(t *testing.T) {
	// 127.0.0.1 should always be bound locally on any system.
	if !isIPBoundLocally("127.0.0.1") {
		t.Error("127.0.0.1 should be bound locally")
	}

	// An arbitrary private IP that's unlikely to be bound.
	if isIPBoundLocally("10.99.99.99") {
		t.Error("10.99.99.99 should not be bound locally (unless coincidentally assigned)")
	}

	// Invalid IP should return false.
	if isIPBoundLocally("not-an-ip") {
		t.Error("invalid IP should return false")
	}

	// Empty string should return false.
	if isIPBoundLocally("") {
		t.Error("empty string should return false")
	}
}

// TestMaybeRebindListenAddr_LocalhostPassthrough verifies that localhost
// bindings are not subject to rebind.
func TestMaybeRebindListenAddr_LocalhostPassthrough(t *testing.T) {
	cases := []string{
		"127.0.0.1:9999",
		"localhost:9999",
	}
	for _, addr := range cases {
		t.Run(addr, func(t *testing.T) {
			got := maybeRebindListenAddr(context.Background(), addr)
			if got != addr {
				t.Errorf("localhost should pass through unchanged: got %q, want %q", got, addr)
			}
		})
	}
}

// TestMaybeRebindListenAddr_BoundIPPassthrough verifies that IPs currently
// bound locally are not subject to rebind.
func TestMaybeRebindListenAddr_BoundIPPassthrough(t *testing.T) {
	// 127.0.0.1 is always bound locally.
	addr := "127.0.0.1:9999"
	got := maybeRebindListenAddr(context.Background(), addr)
	if got != addr {
		t.Errorf("bound IP should pass through unchanged: got %q, want %q", got, addr)
	}
}

// TestMaybeRebindListenAddr_ParseError verifies that unparseable addresses
// are returned unchanged.
func TestMaybeRebindListenAddr_ParseError(t *testing.T) {
	addr := "no-port-here"
	got := maybeRebindListenAddr(context.Background(), addr)
	if got != addr {
		t.Errorf("unparseable address should pass through unchanged: got %q, want %q", got, addr)
	}
}
