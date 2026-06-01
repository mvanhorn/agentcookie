package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/pkg/sidecar"
)

func TestHostMatchesDomain(t *testing.T) {
	cases := []struct {
		host, bare string
		want       bool
	}{
		{"amazon.com", "amazon.com", true},
		{".amazon.com", "amazon.com", true},
		{"www.amazon.com", "amazon.com", true},
		{"sellercentral.amazon.com", "amazon.com", true},
		{"evilamazon.com", "amazon.com", false},
		{"notamazon.com", "amazon.com", false},
		{"amazon-adsystem.com", "amazon.com", false},
		{"amazon.co.uk", "amazon.com", false},
	}
	for _, c := range cases {
		if got := hostMatchesDomain(c.host, c.bare); got != c.want {
			t.Errorf("hostMatchesDomain(%q, %q) = %v, want %v", c.host, c.bare, got, c.want)
		}
	}
}

// makeSidecar writes a Chrome-shaped plaintext sidecar DB and returns its path.
func makeSidecar(t *testing.T, rows [][3]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies-plain.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sidecar: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cookies (
		host_key TEXT, name TEXT, value TEXT, path TEXT,
		expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO cookies (host_key, name, value, path, expires_utc, is_secure, is_httponly) VALUES (?,?,?,'/',0,0,0)`,
			r[0], r[1], r[2]); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	return path
}

func names(cookies []sidecar.Cookie) map[string]bool {
	m := map[string]bool{}
	for _, c := range cookies {
		m[c.Name] = true
	}
	return m
}

func TestCollectDomainCookies_HappyPathAndFiltering(t *testing.T) {
	path := makeSidecar(t, [][3]string{
		{"amazon.com", "session-token", "tok"},
		{".amazon.com", "x-main", "xm"},
		{"www.amazon.com", "ubid", "ub"},
		{"amazon-adsystem.com", "__eoi", "ad"}, // sibling ad domain, must be excluded
		{"amazon.com", "empty", ""},            // empty value, must be skipped
	})
	got, err := collectDomainCookies(path, ".amazon.com", nil)
	if err != nil {
		t.Fatalf("collectDomainCookies: %v", err)
	}
	n := names(got)
	for _, want := range []string{"session-token", "x-main", "ubid"} {
		if !n[want] {
			t.Errorf("expected cookie %q in result, got %v", want, n)
		}
	}
	if n["__eoi"] {
		t.Error("amazon-adsystem.com cookie leaked into .amazon.com result")
	}
	if n["empty"] {
		t.Error("empty-value cookie should be skipped")
	}
	if len(got) != 3 {
		t.Errorf("expected 3 cookies, got %d (%v)", len(got), n)
	}
}

func TestCollectDomainCookies_Blocklist(t *testing.T) {
	path := makeSidecar(t, [][3]string{
		{"amazon.com", "session-token", "tok"},
		{".amazon.com", "x-main", "xm"},
	})
	matcher := protocol.NewBlocklistMatcher(&config.Blocklist{
		Domains: []config.BlocklistEntry{{Pattern: "amazon.com"}, {Pattern: "%.amazon.com"}},
	})
	got, err := collectDomainCookies(path, ".amazon.com", matcher)
	if err != nil {
		t.Fatalf("collectDomainCookies: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("blocklisted domain should yield no cookies, got %d", len(got))
	}
}

func TestCollectDomainCookies_MissingFile(t *testing.T) {
	got, err := collectDomainCookies(filepath.Join(t.TempDir(), "nope.db"), ".amazon.com", nil)
	if err != nil {
		t.Fatalf("missing sidecar should not error, got %v", err)
	}
	if got != nil {
		t.Errorf("missing sidecar should return nil, got %v", got)
	}
}

func TestEmitCookies_Header(t *testing.T) {
	var buf bytes.Buffer
	cookies := []sidecar.Cookie{
		{Name: "a", Value: "1"},
		{Name: "b", Value: "2"},
	}
	if err := emitCookies(&buf, cookies, false); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a=1; b=2\n" {
		t.Errorf("header = %q, want %q", got, "a=1; b=2\n")
	}
}

func TestEmitCookies_JSON(t *testing.T) {
	var buf bytes.Buffer
	cookies := []sidecar.Cookie{{Name: "a", Value: "1", HostKey: ".amazon.com", Path: "/", IsSecure: true}}
	if err := emitCookies(&buf, cookies, true); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, buf.String())
	}
	if len(out) != 1 || out[0]["name"] != "a" || out[0]["value"] != "1" || out[0]["secure"] != true {
		t.Errorf("unexpected JSON shape: %s", buf.String())
	}
}

func TestEmitCookies_EmptyJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := emitCookies(&buf, nil, true); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "[]\n" {
		t.Errorf("empty JSON = %q, want %q", got, "[]\n")
	}
}
