package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
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

	key := []byte("0123456789abcdef")
	dbPath := filepath.Join(t.TempDir(), "Cookies")
	seedSourceCookiesDB(t, dbPath, cookies, key)

	secret := "source-push-test-secret"
	capture := newSourceCapture(t, secret)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = capture
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	cfg := &config.SourceConfig{
		Sink:   config.SinkRef{URL: "http://agentcookie-sink.test/sync"},
		Chrome: config.ChromeRef{DBPath: dbPath},
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
	return pushWithFreshBlocklist(context.Background(), f.cfg, f.key, f.secret, false, false, false, f.srcState, nil)
}

func (f *sourcePushFixture) batchCount() int {
	return f.capture.batchCount()
}

func (f *sourcePushFixture) hostsAt(i int) []string {
	return hostsFromChromeCookies(f.capture.batchAt(i))
}

type sourceCapture struct {
	secret  string
	mu      sync.Mutex
	batches [][]chrome.Cookie
}

func newSourceCapture(t *testing.T, secret string) *sourceCapture {
	t.Helper()
	return &sourceCapture{secret: secret}
}

func (c *sourceCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost {
		return nil, fmt.Errorf("unexpected method %s", req.Method)
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
	c.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewBufferString("ok\n")),
		Request:    req,
	}, nil
}

func (c *sourceCapture) batchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
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
