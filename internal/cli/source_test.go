package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/secretsbus"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

func TestSourcePushReloadsBlocklistBetweenPushes(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains: []
`)

	if _, err := fx.push(); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, []string{".allowed.com", ".blocked.com"}) {
		t.Fatalf("first push hosts = %v", got)
	}

	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.blocked.com"
`)
	if _, err := fx.push(); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if got := fx.hostsAt(1); !reflect.DeepEqual(got, []string{".allowed.com"}) {
		t.Fatalf("second push hosts = %v, want only allowed host after reload", got)
	}
}

func TestSourcePushReloadsAccountsOffStyleBlocklist(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: "example.com", Name: "apex", Value: "a", Path: "/"},
		{HostKey: "www.example.com", Name: "sub", Value: "s", Path: "/"},
		{HostKey: "allowed.com", Name: "allowed", Value: "ok", Path: "/"},
	})

	if _, err := fx.push(); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, []string{"allowed.com", "example.com", "www.example.com"}) {
		t.Fatalf("first push hosts = %v", got)
	}

	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "example.com"
  - pattern: "%.example.com"
`)
	if _, err := fx.push(); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if got := fx.hostsAt(1); !reflect.DeepEqual(got, []string{"allowed.com"}) {
		t.Fatalf("second push hosts = %v, want exact and subdomain dropped", got)
	}
}

func TestSourcePushAllowlistFiltersBeforePush(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: "example.com", Name: "apex", Value: "a", Path: "/"},
		{HostKey: "www.example.com", Name: "sub", Value: "s", Path: "/"},
		{HostKey: "blocked.com", Name: "blocked", Value: "b", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: "example.com"
  - pattern: "%.example.com"
`)

	if _, err := fx.push(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, []string{"example.com", "www.example.com"}) {
		t.Fatalf("allowlist push hosts = %v", got)
	}
}

func TestCDPSourceStatePathIsScopedToConfigDirectory(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "agentcookie-generic")
	if got, want := sourceStatePath(true, configDir, "/irrelevant"), filepath.Join(configDir, "state", "source-state.json"); got != want {
		t.Fatalf("CDP source state path = %q, want %q", got, want)
	}
	if got, want := sourceStatePathForConfig(&config.SourceConfig{CDPSource: config.CDPSourceRef{Enabled: true}}, configDir, "/irrelevant"), filepath.Join(configDir, "state", "source-state.json"); got != want {
		t.Fatalf("configured CDP source state path = %q, want %q", got, want)
	}
	if got, want := sourceStatePathForConfig(nil, configDir, "/home/test"), state.SourcePath("/home/test"); got != want {
		t.Fatalf("default source state path = %q, want %q", got, want)
	}
}

func TestSourcePushCDPSourceAppliesAllowlistWithoutSQLiteFallback(t *testing.T) {
	fx := newSourcePushFixture(t, nil)
	fx.cfg.CDPSource = config.CDPSourceRef{Enabled: true, Endpoint: "http://127.0.0.1:9230"}
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: "example.com"
  - pattern: "%.example.com"
`)

	previous := readCDPSource
	readCDPSource = func(_ context.Context, endpoint string) ([]chrome.Cookie, error) {
		if endpoint != "http://127.0.0.1:9230" {
			t.Fatalf("endpoint = %q", endpoint)
		}
		return []chrome.Cookie{
			{HostKey: "example.com", Name: "apex", Value: "a", Path: "/"},
			{HostKey: "www.example.com", Name: "sub", Value: "s", Path: "/"},
			{HostKey: "blocked.com", Name: "blocked", Value: "b", Path: "/"},
		}, nil
	}
	t.Cleanup(func() { readCDPSource = previous })
	previousSecrets := loadSecretsPayload
	loadSecretsPayload = func(string) (*secretsbus.Payload, []error) {
		t.Fatal("CDP source must not read the machine-wide secrets bus")
		return nil, nil
	}
	t.Cleanup(func() { loadSecretsPayload = previousSecrets })

	if _, err := fx.push(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, []string{"example.com", "www.example.com"}) {
		t.Fatalf("CDP source pushed hosts = %v", got)
	}
	if got := fx.capture.envelopeAt(0).Secrets; len(got) != 0 {
		t.Fatalf("CDP source transported secrets = %v, want none", got)
	}
}

func TestSourcePushEmptyCycleRecordsHealthySourceState(t *testing.T) {
	fx := newSourcePushFixture(t, nil)
	statePath := filepath.Join(t.TempDir(), "source-state.json")
	stateWriter := state.NewWriter(statePath)

	n, err := pushWithFreshBlocklist(context.Background(), fx.cfg, fx.key, false, false, false, fx.srcState, stateWriter)
	if err != nil {
		t.Fatalf("empty push: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty push count = %d, want 0", n)
	}
	if got := fx.batchCount(); got != 0 {
		t.Fatalf("empty push should not POST, got %d requests", got)
	}
	if got := fx.srcState.TotalPushes; got != 1 {
		t.Errorf("TotalPushes = %d, want 1 after a successful empty cycle", got)
	}
	if fx.srcState.LastPush.IsZero() {
		t.Fatal("LastPush should be recorded after a successful empty cycle")
	}
	if got := fx.srcState.LastPushCount; got != 0 {
		t.Errorf("LastPushCount = %d, want 0", got)
	}
	if got := fx.srcState.TotalFailures; got != 0 {
		t.Errorf("TotalFailures = %d, want 0", got)
	}
	persisted, err := state.LoadSource(statePath)
	if err != nil {
		t.Fatalf("load persisted source state: %v", err)
	}
	if persisted == nil {
		t.Fatal("successful empty cycle did not persist source health")
	}
	if got := persisted.TotalPushes; got != 1 {
		t.Errorf("persisted TotalPushes = %d, want 1", got)
	}
	if persisted.LastPush.IsZero() {
		t.Error("persisted LastPush should be recorded after a successful empty cycle")
	}
	if got := persisted.LastPushCount; got != 0 {
		t.Errorf("persisted LastPushCount = %d, want 0", got)
	}
}

func TestSourcePushDryRunDoesNotRecordSourceHealth(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "value", Path: "/"},
	})
	statePath := filepath.Join(t.TempDir(), "source-state.json")
	stateWriter := state.NewWriter(statePath)

	n, err := pushWithFreshBlocklist(context.Background(), fx.cfg, fx.key, true, false, false, fx.srcState, stateWriter)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run push count = %d, want 0", n)
	}
	if got := fx.batchCount(); got != 0 {
		t.Fatalf("dry-run should not POST, got %d requests", got)
	}
	if got := fx.srcState.TotalPushes; got != 0 {
		t.Errorf("dry-run TotalPushes = %d, want 0", got)
	}
	if !fx.srcState.LastPush.IsZero() {
		t.Fatal("dry-run should not update LastPush")
	}
	persisted, err := state.LoadSource(statePath)
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if persisted == nil {
		t.Fatal("dry-run should persist its non-health state envelope")
	}
	if got := persisted.TotalPushes; got != 0 {
		t.Errorf("persisted dry-run TotalPushes = %d, want 0", got)
	}
	if !persisted.LastPush.IsZero() {
		t.Errorf("persisted dry-run should not update LastPush, got %s", persisted.LastPush)
	}
}

func TestSourcePushMalformedBlocklistSkipsPushAndRecordsFailure(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains: []
`)

	if _, err := fx.push(); err != nil {
		t.Fatalf("first push: %v", err)
	}
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains: []
unexpected: true
`)

	n, err := fx.push()
	if err == nil {
		t.Fatal("malformed blocklist should fail closed")
	}
	if n != 0 {
		t.Errorf("malformed blocklist push count = %d, want 0", n)
	}
	if got := fx.batchCount(); got != 1 {
		t.Fatalf("malformed blocklist should not send a second request, got %d batches", got)
	}
	if fx.srcState.TotalFailures != 1 {
		t.Errorf("TotalFailures = %d, want 1", fx.srcState.TotalFailures)
	}
	if fx.srcState.LastError == "" {
		t.Fatal("LastError should be set")
	}
	if fx.srcState.LastErrorAt.IsZero() {
		t.Fatal("LastErrorAt should be set")
	}
}

func TestSourcePushDeletedBlocklistFallsBackToSyncAll(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})
	blocklistPath := filepath.Join(fx.configDir, "blocklist.yaml")
	writeCLIFile(t, blocklistPath, `
version: 1
domains:
  - pattern: "%.blocked.com"
`)

	if _, err := fx.push(); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, []string{".allowed.com"}) {
		t.Fatalf("first push hosts = %v", got)
	}

	if err := os.Remove(blocklistPath); err != nil {
		t.Fatalf("remove blocklist: %v", err)
	}
	if _, err := fx.push(); err != nil {
		t.Fatalf("second push after delete: %v", err)
	}
	if got := fx.hostsAt(1); !reflect.DeepEqual(got, []string{".allowed.com", ".blocked.com"}) {
		t.Fatalf("deleted blocklist hosts = %v, want sync-all", got)
	}
}

func TestSourcePushStableBlocklistFiltersConsistently(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".blocked.com", Name: "blocked", Value: "b", Path: "/"},
		{HostKey: ".allowed.com", Name: "allowed", Value: "a", Path: "/"},
	})
	writeCLIFile(t, filepath.Join(fx.configDir, "blocklist.yaml"), `
version: 1
domains:
  - pattern: "%.blocked.com"
`)

	if _, err := fx.push(); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if _, err := fx.push(); err != nil {
		t.Fatalf("second push: %v", err)
	}
	want := []string{".allowed.com"}
	if got := fx.hostsAt(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("first push hosts = %v, want %v", got, want)
	}
	if got := fx.hostsAt(1); !reflect.DeepEqual(got, want) {
		t.Fatalf("second push hosts = %v, want %v", got, want)
	}
}

// AGENTCOOKIE_SKIP_LOCAL_STORAGE=1 must drop the Local Storage tarball from
// the envelope while delivering the same cookies; the default keeps shipping it.
func TestSourcePushLocalStorageOptOut(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	b, err := chrome.LookupBrowser("")
	if err != nil {
		t.Fatalf("LookupBrowser: %v", err)
	}
	lsDir := b.LocalStorageLevelDB("")
	if err := os.MkdirAll(lsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lsDir, "000003.log"), bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}

	// The default must not depend on an inherited environment.
	t.Setenv(envSkipLocalStorage, "")
	if _, err := fx.push(); err != nil {
		t.Fatalf("default push: %v", err)
	}
	if len(fx.capture.envelopeAt(0).LocalStorageTarball) == 0 {
		t.Fatal("default config should still ship the Local Storage tarball")
	}

	t.Setenv(envSkipLocalStorage, "1")
	if _, err := fx.push(); err != nil {
		t.Fatalf("skip-local-storage push: %v", err)
	}
	if n := len(fx.capture.envelopeAt(1).LocalStorageTarball); n != 0 {
		t.Fatalf("skip-local-storage shipped a %d-byte Local Storage tarball", n)
	}
	before, after := fx.capture.envelopeAt(0).Cookies, fx.capture.envelopeAt(1).Cookies
	if len(before) == 0 {
		t.Fatal("default push carried no cookies")
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("cookies changed with skip-local-storage:\n got %+v\nwant %+v", after, before)
	}
}

// stubSecretsPayload swaps loadSecretsPayload for the test. A nil payload
// makes any call fail the test, so a push that should skip the secrets bus
// proves it never read it.
func stubSecretsPayload(t *testing.T, payload *secretsbus.Payload) {
	t.Helper()
	previous := loadSecretsPayload
	loadSecretsPayload = func(string) (*secretsbus.Payload, []error) {
		if payload == nil {
			t.Fatal("secrets bus was read while --skip-secrets was on")
		}
		return payload, nil
	}
	t.Cleanup(func() { loadSecretsPayload = previous })
}

func withSourceSkipSecrets(t *testing.T, on bool) {
	t.Helper()
	previous := sourceSkipSecrets
	sourceSkipSecrets = on
	t.Cleanup(func() { sourceSkipSecrets = previous })
}

func cachedPullEnvelope(t *testing.T) (protocol.SyncEnvelope, map[string]json.RawMessage) {
	t.Helper()
	raw := pullPayloadCache.Load()
	if len(raw) == 0 {
		t.Fatal("push did not publish a pull envelope")
	}
	var env protocol.SyncEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal cached envelope: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal cached envelope fields: %v", err)
	}
	return env, fields
}

func TestSourcePushSecretsOptOut(t *testing.T) {
	cookies := []chrome.Cookie{{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"}}
	oneCLI := &secretsbus.Payload{CLIs: map[string]map[string]string{"demo-cli": {"DEMO_TOKEN": "t0k"}}}

	t.Run("env 1 never reads the secrets bus", func(t *testing.T) {
		fx := newSourcePushFixture(t, cookies)
		withSourceSkipSecrets(t, false)
		t.Setenv(envSkipSecrets, "1")
		stubSecretsPayload(t, nil)

		if _, err := fx.push(); err != nil {
			t.Fatalf("push: %v", err)
		}
		if got := fx.capture.envelopeAt(0).Secrets; len(got) != 0 {
			t.Fatalf("envelope carried secrets %v with --skip-secrets", got)
		}
		if len(fx.capture.envelopeAt(0).Cookies) == 0 {
			t.Fatal("skip-secrets push carried no cookies")
		}
		env, fields := cachedPullEnvelope(t)
		if len(env.Secrets) != 0 {
			t.Fatalf("GET /pull cache carried secrets %v", env.Secrets)
		}
		if _, ok := fields["secrets"]; ok {
			t.Fatal("GET /pull cache has a secrets field")
		}
		if !fx.srcState.LastSecretsSkipped {
			t.Error("source state should record that secrets were skipped")
		}
		if fx.srcState.LastSecretsCLIs != 0 {
			t.Errorf("LastSecretsCLIs = %d, want 0", fx.srcState.LastSecretsCLIs)
		}
	})

	t.Run("env cleared ships secrets", func(t *testing.T) {
		fx := newSourcePushFixture(t, cookies)
		withSourceSkipSecrets(t, false)
		// The default must not depend on an inherited environment.
		t.Setenv(envSkipSecrets, "")
		stubSecretsPayload(t, oneCLI)

		if _, err := fx.push(); err != nil {
			t.Fatalf("push: %v", err)
		}
		if got := fx.capture.envelopeAt(0).Secrets; !reflect.DeepEqual(got, oneCLI.CLIs) {
			t.Fatalf("envelope secrets = %v, want %v", got, oneCLI.CLIs)
		}
		if env, _ := cachedPullEnvelope(t); !reflect.DeepEqual(env.Secrets, oneCLI.CLIs) {
			t.Fatalf("GET /pull cache secrets = %v, want %v", env.Secrets, oneCLI.CLIs)
		}
		if fx.srcState.LastSecretsSkipped {
			t.Error("source state recorded skipped without the switch")
		}
		if fx.srcState.LastSecretsCLIs != 1 {
			t.Errorf("LastSecretsCLIs = %d, want 1", fx.srcState.LastSecretsCLIs)
		}
	})

	t.Run("flag alone skips", func(t *testing.T) {
		fx := newSourcePushFixture(t, cookies)
		t.Setenv(envSkipSecrets, "")
		withSourceSkipSecrets(t, true)
		stubSecretsPayload(t, nil)

		if _, err := fx.push(); err != nil {
			t.Fatalf("push: %v", err)
		}
		if got := fx.capture.envelopeAt(0).Secrets; len(got) != 0 {
			t.Fatalf("envelope carried secrets %v with --skip-secrets", got)
		}
	})
}

func TestSkipSecretsFailsClosed(t *testing.T) {
	withSourceSkipSecrets(t, false)
	for _, tc := range []struct {
		value    string
		wantSkip bool
		wantWarn bool
	}{
		{value: "", wantSkip: false},
		{value: "0", wantSkip: false},
		{value: "1", wantSkip: true},
		{value: "true", wantSkip: true, wantWarn: true},
		{value: "no", wantSkip: true, wantWarn: true},
	} {
		t.Run(fmt.Sprintf("%q", tc.value), func(t *testing.T) {
			resetSkipSecretsWarning()
			t.Setenv(envSkipSecrets, tc.value)
			var got [2]bool
			out := captureStderr(t, func() {
				got[0] = skipSecrets()
				got[1] = skipSecrets()
			})
			if got[0] != tc.wantSkip || got[1] != tc.wantSkip {
				t.Fatalf("skipSecrets() = %v, want %v", got, tc.wantSkip)
			}
			if !tc.wantWarn {
				if out != "" {
					t.Fatalf("unexpected stderr %q", out)
				}
				return
			}
			if n := strings.Count(out, "\n"); n != 1 {
				t.Fatalf("want one warning line, got %d: %q", n, out)
			}
			if !strings.Contains(out, fmt.Sprintf("%q", tc.value)) || !strings.Contains(out, envSkipSecrets) {
				t.Fatalf("warning should name %s and the value %q, got %q", envSkipSecrets, tc.value, out)
			}
		})
	}
}

func TestSecretsBusWatchersSkipped(t *testing.T) {
	fire := func(context.Context) {}
	if got := secretsBusWatchers(true, t.TempDir(), false, fire); len(got) != 0 {
		t.Fatalf("--skip-secrets constructed %d secrets-bus watcher(s), want none", len(got))
	}
	got := secretsBusWatchers(false, t.TempDir(), false, fire)
	names := make([]string, 0, len(got))
	for _, w := range got {
		names = append(names, w.name)
	}
	if want := []string{"secrets-bus watcher", "discovery watcher"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("watchers = %v, want %v", names, want)
	}
}

type sourcePushFixture struct {
	configDir string
	cfg       *config.SourceConfig
	key       []byte
	secret    string
	capture   *sourceCapture
	srcState  *state.SourceState
}

func newSourcePushFixture(t *testing.T, cookies []chrome.Cookie) *sourcePushFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := t.TempDir()
	withConfigDir(t, configDir)

	// Never consult the real tailnet: a sink host that happens to match a
	// live Tailscale peer (for example "instinct") would be rewritten to its
	// IP. Tests that exercise resolution install their own resolver.
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, rawURL string) (string, error) {
		return rawURL, nil
	}))

	key := []byte("0123456789abcdef")
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	seedSourceCookiesDB(t, dbPath, cookies, key)

	secret := "source-push-test-secret"
	capture := newSourceCapture(t, secret)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = capture
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	// Never shell out to the real Tailscale CLI: status is unavailable
	// unless a test installs its own stub.
	t.Cleanup(tsclient.SetStatusForTesting(func(context.Context) (*tsclient.Status, error) {
		return nil, errors.New("tailscale status unavailable in tests")
	}))

	cfg := &config.SourceConfig{
		Sink:     config.SinkRef{URL: "http://agentcookie-sink.test/sync"},
		Chrome:   config.ChromeRef{DBPath: dbPath},
		Security: config.SecurityRef{SharedSecret: secret},
	}
	return &sourcePushFixture{
		configDir: configDir,
		cfg:       cfg,
		key:       key,
		secret:    secret,
		capture:   capture,
		srcState:  &state.SourceState{Role: "source", SinkURL: "http://agentcookie-sink.test/sync"},
	}
}

func (f *sourcePushFixture) push() (int, error) {
	return pushWithFreshBlocklist(context.Background(), f.cfg, f.key, false, false, false, f.srcState, nil)
}

func (f *sourcePushFixture) batchCount() int {
	return f.capture.batchCount()
}

func (f *sourcePushFixture) hostsAt(i int) []string {
	return hostsFromChromeCookies(f.capture.batchAt(i))
}

type sourceCapture struct {
	secret    string
	mu        sync.Mutex
	batches   [][]chrome.Cookie
	urls      []string
	envelopes []protocol.SyncEnvelope
	// failURL, when non-empty, makes a POST to that exact URL return a
	// non-200 so tests can exercise per-sink failure isolation.
	failURL string
	// blockURL, when non-empty, makes a POST to that exact URL hang until
	// the request context ends, standing in for a dead sink.
	blockURL string
	// attempts counts every POST per URL, including failed ones.
	attempts map[string]int
	// arrivals records when each URL's most recent POST reached the transport.
	arrivals map[string]time.Time
}

func newSourceCapture(t *testing.T, secret string) *sourceCapture {
	t.Helper()
	return &sourceCapture{secret: secret}
}

func (c *sourceCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost {
		return nil, fmt.Errorf("unexpected method %s", req.Method)
	}
	c.mu.Lock()
	if c.attempts == nil {
		c.attempts = map[string]int{}
		c.arrivals = map[string]time.Time{}
	}
	c.attempts[req.URL.String()]++
	c.arrivals[req.URL.String()] = time.Now()
	c.mu.Unlock()
	if c.blockURL != "" && req.URL.String() == c.blockURL {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	if c.failURL != "" && req.URL.String() == c.failURL {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString("boom\n")),
			Request:    req,
		}, nil
	}
	sealed, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	plaintext, err := transport.OpenWithSecret(sealed, c.secret)
	if err != nil {
		return nil, fmt.Errorf("open payload: %w", err)
	}
	var envelope protocol.SyncEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}
	c.mu.Lock()
	c.batches = append(c.batches, append([]chrome.Cookie(nil), envelope.Cookies...))
	c.urls = append(c.urls, req.URL.String())
	c.envelopes = append(c.envelopes, envelope)
	c.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString("ok\n")),
		Request:    req,
	}, nil
}

func (c *sourceCapture) postedURLs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.urls...)
}

func (c *sourceCapture) attemptsTo(url string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[url]
}

func (c *sourceCapture) arrivalAt(url string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.arrivals[url]
	return at, ok
}

func (c *sourceCapture) batchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

func (c *sourceCapture) envelopeAt(i int) protocol.SyncEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.envelopes[i]
}

func (c *sourceCapture) batchAt(i int) []chrome.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]chrome.Cookie(nil), c.batches[i]...)
}

func seedSourceCookiesDB(t *testing.T, path string, cookies []chrome.Cookie, key []byte) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if _, err := db.Exec(sourceCookieSchema); err != nil {
		_ = db.Close()
		t.Fatalf("seed schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}
	if _, err := chrome.WriteCookies(path, cookies, key); err != nil {
		t.Fatalf("seed cookies: %v", err)
	}
}

func hostsFromChromeCookies(cookies []chrome.Cookie) []string {
	hosts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		hosts = append(hosts, c.HostKey)
	}
	sort.Strings(hosts)
	return hosts
}

const sourceCookieSchema = `
CREATE TABLE IF NOT EXISTS cookies(
	creation_utc INTEGER NOT NULL,
	host_key TEXT NOT NULL,
	top_frame_site_key TEXT NOT NULL,
	name TEXT NOT NULL,
	value TEXT NOT NULL,
	encrypted_value BLOB NOT NULL,
	path TEXT NOT NULL,
	expires_utc INTEGER NOT NULL,
	is_secure INTEGER NOT NULL,
	is_httponly INTEGER NOT NULL,
	last_access_utc INTEGER NOT NULL,
	has_expires INTEGER NOT NULL,
	is_persistent INTEGER NOT NULL,
	priority INTEGER NOT NULL,
	samesite INTEGER NOT NULL,
	source_scheme INTEGER NOT NULL,
	source_port INTEGER NOT NULL,
	last_update_utc INTEGER NOT NULL,
	source_type INTEGER NOT NULL,
	has_cross_site_ancestor INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS cookies_unique_index ON cookies(
	host_key, top_frame_site_key, has_cross_site_ancestor, name, path, source_scheme, source_port
);
`

// TestSourcePushAmbiguousPeerAbortsWithoutPOST verifies that when ResolveSinkURL
// returns ErrAmbiguousPeer (multiple online peers share the hostname), the push
// fails closed and does NOT fall back to the hostname URL. Falling back would
// hand selection to MagicDNS and undo the fail-closed behavior.
func TestSourcePushAmbiguousPeerAbortsWithoutPOST(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})

	// Override the resolver to return ErrAmbiguousPeer.
	restore := SetResolveSinkURLForTesting(func(ctx context.Context, rawURL string) (string, error) {
		return rawURL, fmt.Errorf("%w: \"grok-bot\" has 2 online nodes with IPs [100.87.49.2 100.124.19.34]; pin sink.url to one 100.x IP or delete the leftover node from Tailscale admin",
			tsclient.ErrAmbiguousPeer)
	})
	defer restore()

	n, err := fx.push()
	if err == nil {
		t.Fatal("ambiguous peer should fail closed, got nil error")
	}
	if !errors.Is(err, tsclient.ErrAmbiguousPeer) {
		t.Errorf("error should wrap ErrAmbiguousPeer, got: %v", err)
	}
	if !strings.Contains(err.Error(), "resolve sink URL") {
		t.Errorf("error should mention 'resolve sink URL', got: %v", err)
	}
	if n != 0 {
		t.Errorf("ambiguous peer push count = %d, want 0", n)
	}
	if got := fx.batchCount(); got != 0 {
		t.Fatalf("ambiguous peer should NOT send any HTTP request, got %d batches", got)
	}
	if fx.srcState.TotalFailures != 1 {
		t.Errorf("TotalFailures = %d, want 1", fx.srcState.TotalFailures)
	}
}

// TestSourcePushSoftResolveErrorFallsBackToHostname verifies that soft failures
// (Tailscale CLI missing, peer not found) fall back to the original URL and
// proceed with the POST, letting HTTP report the connection error. A peer
// Tailscale reports offline is skipped instead; see
// TestSourcePushOfflinePeerIsSkippedAsAsleep.
func TestSourcePushSoftResolveErrorFallsBackToHostname(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "session", Value: "xyz", Path: "/"},
	})

	// Override the resolver to return ErrPeerNotFound (a soft failure).
	restore := SetResolveSinkURLForTesting(func(ctx context.Context, rawURL string) (string, error) {
		return rawURL, fmt.Errorf("%w: \"unknown-host\"", tsclient.ErrPeerNotFound)
	})
	defer restore()

	// Push should succeed (fall back to original URL, HTTP capture accepts it).
	n, err := fx.push()
	if err != nil {
		t.Fatalf("soft resolve error should fall back to original URL, got: %v", err)
	}
	if n != 1 {
		t.Errorf("push count = %d, want 1", n)
	}
	if got := fx.batchCount(); got != 1 {
		t.Fatalf("soft resolve error should still POST, got %d batches", got)
	}
}

// --- Multi-sink fan-out (U2) and per-sink state (U3) ---

func TestSourcePushFansOutToMultipleSinks(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	// Two peerless sinks sharing the legacy secret (fixture bypasses
	// LoadSource, which would otherwise require a per-sink credential).
	urlA := "http://sink-a.test/sync"
	urlB := "http://sink-b.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlA}, {URL: urlB}}

	if _, err := fx.push(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := fx.batchCount(); got != 2 {
		t.Fatalf("expected 2 POSTs (one per sink), got %d", got)
	}
	posted := fx.capture.postedURLs()
	seen := map[string]bool{}
	for _, u := range posted {
		seen[u] = true
	}
	if !seen[urlA] || !seen[urlB] {
		t.Fatalf("both sinks should have received the payload, got %v", posted)
	}
	// Per-sink state: two records, both with a successful push.
	if n := len(fx.srcState.Sinks); n != 2 {
		t.Fatalf("expected 2 per-sink state records, got %d", n)
	}
	for _, ps := range fx.srcState.Sinks {
		if ps.TotalPushes != 1 || ps.TotalFailures != 0 {
			t.Errorf("sink %s: expected 1 push 0 failures, got %+v", ps.URL, ps)
		}
	}
}

func TestSourcePushIsolatesFailedSink(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlBad := "http://sink-bad.test/sync"
	urlGood := "http://sink-good.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlBad}, {URL: urlGood}}
	fx.capture.failURL = urlBad

	_, err := fx.push()
	if err == nil {
		t.Fatal("expected partial-failure error, got nil")
	}
	// The healthy sink still received the payload.
	posted := fx.capture.postedURLs()
	if len(posted) != 1 || posted[0] != urlGood {
		t.Fatalf("healthy sink should have been delivered exactly once, got %v", posted)
	}
	// Per-sink state records the failure on the bad sink, success on the good.
	var bad, good *state.SinkPushState
	for i := range fx.srcState.Sinks {
		switch fx.srcState.Sinks[i].URL {
		case urlBad:
			bad = &fx.srcState.Sinks[i]
		case urlGood:
			good = &fx.srcState.Sinks[i]
		}
	}
	if bad == nil || bad.TotalFailures != 1 {
		t.Errorf("bad sink should have 1 failure, got %+v", bad)
	}
	if good == nil || good.TotalPushes != 1 {
		t.Errorf("good sink should have 1 push, got %+v", good)
	}
}

func TestSourcePushMissingKeyIsolatedNoSilentDowngrade(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	// One peer'd sink whose key is absent from the keystore, alongside a
	// healthy peerless sink covered by the legacy shared secret. The peer'd
	// sink must NOT be sealed under the shared secret (no silent downgrade):
	// it fails, and only the peerless sink is POSTed.
	urlPeer := "http://sink-peer.test/sync"
	urlShared := "http://sink-shared.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{
		{URL: urlPeer, Peer: "no-such-peer"},
		{URL: urlShared},
	}

	_, err := fx.push()
	if err == nil {
		t.Fatal("expected error for missing per-sink key, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-peer") {
		t.Errorf("error should name the peer with the missing key, got: %v", err)
	}
	posted := fx.capture.postedURLs()
	if len(posted) != 1 || posted[0] != urlShared {
		t.Fatalf("only the shared-secret sink should be posted (no downgrade of the peer'd sink), got %v", posted)
	}
}

// --- Push retry backoff (KTD11) ---

// sinkStateFor returns the per-sink record for url, or nil.
func sinkStateFor(st *state.SourceState, url string) *state.SinkPushState {
	for i := range st.Sinks {
		if st.Sinks[i].URL == url {
			return &st.Sinks[i]
		}
	}
	return nil
}

// stubSourceClock pins sourceNow to a settable fake time.
func stubSourceClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	prev := sourceNow
	sourceNow = func() time.Time { return now }
	t.Cleanup(func() { sourceNow = prev })
	return &now
}

func TestSourcePushOfflinePeerIsSkippedAsAsleep(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlAsleep := "http://instinct.test/sync"
	urlAwake := "http://grok-bot.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlAsleep}, {URL: urlAwake}}
	restore := SetResolveSinkURLForTesting(func(ctx context.Context, rawURL string) (string, error) {
		if rawURL == urlAsleep {
			return rawURL, fmt.Errorf("%w: \"instinct\" (all 1 matching nodes are offline)", tsclient.ErrPeerOffline)
		}
		return rawURL, nil
	})
	defer restore()

	if _, err := fx.push(); err != nil {
		t.Fatalf("an offline peer is not a push failure, got: %v", err)
	}
	if n := fx.capture.attemptsTo(urlAsleep); n != 0 {
		t.Fatalf("offline peer must not be dialed, got %d POSTs", n)
	}
	if n := fx.capture.attemptsTo(urlAwake); n != 1 {
		t.Fatalf("online peer should get 1 POST, got %d", n)
	}
	ps := sinkStateFor(fx.srcState, urlAsleep)
	if ps == nil {
		t.Fatal("offline peer should have a per-sink record")
	}
	if !ps.Asleep || ps.LastAsleepAt.IsZero() {
		t.Errorf("offline peer should be recorded asleep, got %+v", ps)
	}
	if ps.TotalFailures != 0 || ps.ConsecutiveFailures != 0 || !ps.BackoffUntil.IsZero() {
		t.Errorf("asleep is not a failure: got %+v", ps)
	}
	if fx.srcState.TotalFailures != 0 {
		t.Errorf("source TotalFailures = %d, want 0", fx.srcState.TotalFailures)
	}
}

func TestSourcePushBackoffScheduleCapsAndResetsOnSuccess(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlBad := "http://sink-bad.test/sync"
	urlGood := "http://sink-good.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlBad}, {URL: urlGood}}
	fx.capture.failURL = urlBad
	restore := SetResolveSinkURLForTesting(func(ctx context.Context, rawURL string) (string, error) {
		return rawURL, nil
	})
	defer restore()
	now := stubSourceClock(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))

	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute,
	}
	for i, delay := range want {
		if _, err := fx.push(); err == nil {
			t.Fatalf("failure %d: expected push error", i+1)
		}
		if got := fx.capture.attemptsTo(urlBad); got != i+1 {
			t.Fatalf("failure %d: bad sink dialed %d times, want %d", i+1, got, i+1)
		}
		ps := sinkStateFor(fx.srcState, urlBad)
		if ps.ConsecutiveFailures != i+1 {
			t.Fatalf("failure %d: ConsecutiveFailures = %d", i+1, ps.ConsecutiveFailures)
		}
		if got := ps.BackoffUntil.Sub(*now); got != delay {
			t.Fatalf("failure %d: backoff = %s, want %s", i+1, got, delay)
		}
		// A push inside the window skips the bad sink but still serves the
		// healthy one, and is not an error.
		*now = now.Add(delay - time.Second)
		if _, err := fx.push(); err != nil {
			t.Fatalf("failure %d: push inside backoff window should skip, got %v", i+1, err)
		}
		if got := fx.capture.attemptsTo(urlBad); got != i+1 {
			t.Fatalf("failure %d: bad sink dialed inside its backoff window", i+1)
		}
		*now = now.Add(time.Second)
	}
	if got := sinkStateFor(fx.srcState, urlBad).TotalFailures; got != len(want) {
		t.Errorf("TotalFailures = %d, want %d (skips are not failures)", got, len(want))
	}
	if got := sinkStateFor(fx.srcState, urlGood).TotalPushes; got != 2*len(want) {
		t.Errorf("healthy sink pushes = %d, want %d", got, 2*len(want))
	}

	// Recovery resets the schedule.
	fx.capture.failURL = ""
	if _, err := fx.push(); err != nil {
		t.Fatalf("recovered push: %v", err)
	}
	ps := sinkStateFor(fx.srcState, urlBad)
	if ps.ConsecutiveFailures != 0 || !ps.BackoffUntil.IsZero() {
		t.Fatalf("success should reset backoff, got %+v", ps)
	}
	fx.capture.failURL = urlBad
	_, _ = fx.push()
	if got := sinkStateFor(fx.srcState, urlBad).BackoffUntil.Sub(*now); got != want[0] {
		t.Fatalf("first failure after reset: backoff = %s, want %s", got, want[0])
	}
}

func TestSourcePushDeadSinkDoesNotDelayOthers(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlDead := "http://sink-dead.test/sync"
	urlLive := "http://sink-live.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlDead}, {URL: urlLive}}
	fx.capture.blockURL = urlDead
	restore := SetResolveSinkURLForTesting(func(ctx context.Context, rawURL string) (string, error) {
		return rawURL, nil
	})
	defer restore()

	budget := 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	_, err := pushWithFreshBlocklist(ctx, fx.cfg, fx.key, false, false, false, fx.srcState, nil)
	if err == nil {
		t.Fatal("dead sink should report a failure")
	}
	at, ok := fx.capture.arrivalAt(urlLive)
	if !ok {
		t.Fatal("live sink was never pushed")
	}
	if wait := at.Sub(start); wait > budget/2 {
		t.Fatalf("live sink waited %s behind the dead one", wait)
	}
	if ps := sinkStateFor(fx.srcState, urlLive); ps == nil || ps.TotalPushes != 1 {
		t.Fatalf("live sink should record 1 push, got %+v", ps)
	}
	if ps := sinkStateFor(fx.srcState, urlDead); ps == nil || ps.TotalFailures != 1 {
		t.Fatalf("dead sink should record 1 failure, got %+v", ps)
	}
}

func TestPushTriggerRateCapCollapsesBursts(t *testing.T) {
	now := stubSourceClock(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	var pushes int
	trigger := newRateCappedTrigger(pushTriggerMinInterval, func(context.Context) { pushes++ })

	// A burst from the secrets and discovery watchers sharing one trigger.
	for range 20 {
		trigger.fire(context.Background())
		*now = now.Add(100 * time.Millisecond)
	}
	if pushes != 1 {
		t.Fatalf("burst of 20 triggers ran %d pushes, want 1", pushes)
	}
	*now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Add(pushTriggerMinInterval - time.Millisecond)
	trigger.fire(context.Background())
	if pushes != 1 {
		t.Fatalf("trigger inside the cap ran a push (%d)", pushes)
	}
	*now = now.Add(time.Millisecond)
	trigger.fire(context.Background())
	if pushes != 2 {
		t.Fatalf("trigger after the cap should push, got %d", pushes)
	}
}

// --- Backoff survives restarts ---

// passthroughResolver keeps the sink URL as-is so no test reaches Tailscale.
func passthroughResolver(t *testing.T) {
	t.Helper()
	t.Cleanup(SetResolveSinkURLForTesting(func(_ context.Context, rawURL string) (string, error) {
		return rawURL, nil
	}))
}

// runSourceOnceCDP runs `agentcookie source --once` end to end against a CDP
// source config in fx.configDir, so the run never touches the Keychain.
func runSourceOnceCDP(t *testing.T, fx *sourcePushFixture, sinkURLs ...string) error {
	t.Helper()
	var b strings.Builder
	b.WriteString("cdp_source:\n  enabled: true\n  endpoint: \"http://127.0.0.1:9230\"\n")
	b.WriteString("security:\n  shared_secret: \"" + fx.secret + "\"\n")
	b.WriteString("sinks:\n")
	for _, u := range sinkURLs {
		b.WriteString("  - url: \"" + u + "\"\n")
	}
	writeCLIFile(t, filepath.Join(fx.configDir, "source.yaml"), b.String())

	previous := readCDPSource
	readCDPSource = func(context.Context, string) ([]chrome.Cookie, error) {
		return []chrome.Cookie{{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"}}, nil
	}
	t.Cleanup(func() { readCDPSource = previous })

	prevOnce, prevWatch := sourceOnce, sourceWatch
	sourceOnce, sourceWatch = true, false
	t.Cleanup(func() { sourceOnce, sourceWatch = prevOnce, prevWatch })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return runSource(cmd, nil)
}

func TestSourceOnceHonorsBackoffFromPreviousRun(t *testing.T) {
	fx := newSourcePushFixture(t, nil)
	fx.secret = "source-once-restart-test-secret-0123456789"
	fx.capture.secret = fx.secret
	passthroughResolver(t)
	now := stubSourceClock(t, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))

	urlBacked := "http://sink-backed-off.test/sync"
	urlFresh := "http://sink-fresh.test/sync"
	statePath := sourceStatePath(true, fx.configDir, "")
	until := now.Add(10 * time.Minute)
	prev := &state.SourceState{
		Role: "source",
		Sinks: []state.SinkPushState{{
			URL:                 urlBacked,
			TotalFailures:       4,
			ConsecutiveFailures: 4,
			BackoffUntil:        until,
			LastError:           "sink returned 500",
		}},
	}
	if err := state.NewWriter(statePath).Save(prev); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := runSourceOnceCDP(t, fx, urlBacked, urlFresh); err != nil {
		t.Fatalf("source --once: %v", err)
	}
	if n := fx.capture.attemptsTo(urlBacked); n != 0 {
		t.Fatalf("sink inside a persisted backoff window was dialed %d times", n)
	}
	if n := fx.capture.attemptsTo(urlFresh); n != 1 {
		t.Fatalf("fresh sink should get 1 POST, got %d", n)
	}
	saved, err := state.LoadSource(statePath)
	if err != nil || saved == nil {
		t.Fatalf("reload state: %v", err)
	}
	ps := sinkStateFor(saved, urlBacked)
	if ps == nil || ps.ConsecutiveFailures != 4 || ps.TotalFailures != 4 || !ps.BackoffUntil.Equal(until) {
		t.Fatalf("backed-off sink record should carry over unchanged, got %+v", ps)
	}
}

func TestSourceOnceCorruptStateFileStartsFresh(t *testing.T) {
	fx := newSourcePushFixture(t, nil)
	fx.secret = "source-once-restart-test-secret-0123456789"
	fx.capture.secret = fx.secret
	passthroughResolver(t)

	urlSink := "http://sink-a.test/sync"
	statePath := sourceStatePath(true, fx.configDir, "")
	writeCLIFile(t, statePath, "{not json")

	if err := runSourceOnceCDP(t, fx, urlSink); err != nil {
		t.Fatalf("source --once with a corrupt state file: %v", err)
	}
	if n := fx.capture.attemptsTo(urlSink); n != 1 {
		t.Fatalf("sink should be dialed once, got %d", n)
	}
	saved, err := state.LoadSource(statePath)
	if err != nil || saved == nil {
		t.Fatalf("state file should be rewritten cleanly: %v", err)
	}
	if ps := sinkStateFor(saved, urlSink); ps == nil || ps.TotalPushes != 1 || ps.ConsecutiveFailures != 0 {
		t.Fatalf("fresh per-sink record expected, got %+v", ps)
	}
}

func TestNewSourceStateLoadsOldFileWithoutBackoffFields(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "source-state.json")
	writeCLIFile(t, statePath, `{"role":"source","sink_url":"http://a.test/sync","sinks":[{"peer":"grok-bot","url":"http://a.test/sync","total_pushes":7,"total_failures":2}]}`)

	st := newSourceState(statePath, []config.SinkTarget{{URL: "http://a.test/sync", Peer: "grok-bot"}})
	if len(st.Sinks) != 1 {
		t.Fatalf("expected 1 carried-over record, got %+v", st.Sinks)
	}
	ps := st.Sinks[0]
	if ps.Peer != "grok-bot" || ps.TotalPushes != 7 || ps.TotalFailures != 2 {
		t.Fatalf("history should carry over, got %+v", ps)
	}
	if ps.ConsecutiveFailures != 0 || !ps.BackoffUntil.IsZero() || ps.Asleep || !ps.LastAsleepAt.IsZero() {
		t.Fatalf("missing new fields should decode as zero values, got %+v", ps)
	}

	if missing := newSourceState(filepath.Join(t.TempDir(), "absent.json"), nil); len(missing.Sinks) != 0 || missing.Role != "source" {
		t.Fatalf("missing file should start fresh, got %+v", missing)
	}
}

func TestNewSourceStateDropsRemovedSinks(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "source-state.json")
	writeCLIFile(t, statePath, `{"role":"source","sinks":[
		{"peer":"kept","url":"http://old-url.test/sync","total_pushes":3},
		{"peer":"retired","url":"http://retired.test/sync","total_failures":4,"last_error":"refused"},
		{"url":"http://peerless.test/sync","total_pushes":2},
		{"url":"http://gone-peerless.test/sync","total_pushes":1}
	]}`)

	st := newSourceState(statePath, []config.SinkTarget{
		{URL: "http://new-url.test/sync", Peer: "kept"},
		{URL: "http://peerless.test/sync"},
	})
	if st.SinkURL != "http://new-url.test/sync" {
		t.Fatalf("legacy SinkURL should be the first configured sink, got %q", st.SinkURL)
	}
	if len(st.Sinks) != 2 {
		t.Fatalf("only configured sinks should carry over, got %+v", st.Sinks)
	}
	if st.Sinks[0].Peer != "kept" || st.Sinks[0].TotalPushes != 3 {
		t.Fatalf("peer-keyed record should survive a URL change, got %+v", st.Sinks[0])
	}
	if st.Sinks[1].URL != "http://peerless.test/sync" || st.Sinks[1].TotalPushes != 2 {
		t.Fatalf("peerless record should match by URL, got %+v", st.Sinks[1])
	}
}

// --- Literal Tailscale IP sinks ---

func TestSourcePushOfflineTailnetIPSinkIsSkippedAsAsleep(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlAsleep := "http://100.101.102.103:9999/sync"
	urlAwake := "http://100.87.49.2:9999/sync"
	urlV6Asleep := "http://[fd7a:115c:a1e0::1234]:9999/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlAsleep}, {URL: urlAwake}, {URL: urlV6Asleep}}
	passthroughResolver(t)
	calls := 0
	t.Cleanup(tsclient.SetStatusForTesting(func(context.Context) (*tsclient.Status, error) {
		calls++
		return &tsclient.Status{Peer: map[string]*tsclient.PeerStatus{
			"a": {HostName: "instinct", TailscaleIPs: []string{"100.101.102.103"}, Online: false},
			"b": {HostName: "grok-bot", TailscaleIPs: []string{"100.87.49.2"}, Online: true},
			"c": {HostName: "sandbox", TailscaleIPs: []string{"100.90.1.1", "fd7a:115c:a1e0::1234"}, Online: false},
		}}, nil
	}))

	if _, err := fx.push(); err != nil {
		t.Fatalf("an offline peer is not a push failure, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("Tailscale status should be fetched once per push, got %d", calls)
	}
	for _, u := range []string{urlAsleep, urlV6Asleep} {
		if n := fx.capture.attemptsTo(u); n != 0 {
			t.Fatalf("offline IP peer %s must not be dialed, got %d POSTs", u, n)
		}
		ps := sinkStateFor(fx.srcState, u)
		if ps == nil || !ps.Asleep || ps.LastAsleepAt.IsZero() || ps.TotalFailures != 0 || !ps.BackoffUntil.IsZero() {
			t.Fatalf("offline IP peer %s should be recorded asleep, not failed, got %+v", u, ps)
		}
	}
	if n := fx.capture.attemptsTo(urlAwake); n != 1 {
		t.Fatalf("online IP peer should get 1 POST, got %d", n)
	}
}

func TestSourcePushTailnetIPSinkDialsWhenStatusUnavailable(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	urlSink := "http://100.101.102.103:9999/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: urlSink}}
	passthroughResolver(t)
	// The fixture's status stub already reports Tailscale unavailable.

	if _, err := fx.push(); err != nil {
		t.Fatalf("push: %v", err)
	}
	if n := fx.capture.attemptsTo(urlSink); n != 1 {
		t.Fatalf("status unavailable should still dial the IP sink once, got %d", n)
	}
	if ps := sinkStateFor(fx.srcState, urlSink); ps == nil || ps.Asleep || ps.TotalPushes != 1 {
		t.Fatalf("expected a normal push record, got %+v", ps)
	}
}
