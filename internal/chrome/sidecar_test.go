package chrome

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCookiesSidecar_HappyPath(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cookies-plain.db")
	cookies := []Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "plain-text-session-token", Path: "/", IsSecure: 1, SameSite: 0, HasExpires: 1, ExpiresUTC: 13380163200000000},
		{HostKey: "github.com", Name: "user_session", Value: "github-token-12345", Path: "/", IsSecure: 1},
		{HostKey: ".claude.ai", Name: "csrf", Value: "csrf-token", SameSite: 1},
	}
	n, err := WriteCookiesSidecar(target, cookies, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("wrote %d, want 3", n)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sidecar mode: got %o, want 0600", info.Mode().Perm())
	}

	db, err := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("select count(*) from cookies").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("sqlite count: got %d, want 3", count)
	}

	rows, _ := db.Query("select host_key, name, value, length(encrypted_value) from cookies where name = '_session'")
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected at least one row for _session")
	}
	var host, name, value string
	var encLen int
	if err := rows.Scan(&host, &name, &value, &encLen); err != nil {
		t.Fatal(err)
	}
	if host != ".instacart.com" || name != "_session" {
		t.Errorf("row identity wrong: %s/%s", host, name)
	}
	if value != "plain-text-session-token" {
		t.Errorf("value not plaintext: %q", value)
	}
	if encLen != 0 {
		t.Errorf("encrypted_value should be empty, got %d bytes", encLen)
	}
}

func TestWriteCookiesSidecar_AtomicReplace(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cookies-plain.db")
	if _, err := WriteCookiesSidecar(target, []Cookie{
		{HostKey: "a.com", Name: "first", Value: "old"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCookiesSidecar(target, []Cookie{
		{HostKey: "b.com", Name: "second", Value: "new"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	defer db.Close()
	rows, _ := db.Query("select host_key, name, value from cookies")
	defer rows.Close()
	count := 0
	for rows.Next() {
		var h, n, v string
		rows.Scan(&h, &n, &v)
		if h != "b.com" {
			t.Errorf("expected only b.com row after replace, got %s", h)
		}
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 row after replace, got %d", count)
	}
}

func TestWriteCookiesSidecar_ParentDirCreated(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "sub", "cookies.db")
	if _, err := WriteCookiesSidecar(target, []Cookie{{HostKey: "x.com", Name: "n"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("sidecar not created: %v", err)
	}
	info, _ := os.Stat(filepath.Dir(target))
	if info.Mode().Perm() != 0o700 {
		t.Errorf("parent dir mode: got %o, want 0700", info.Mode().Perm())
	}
}

func TestWriteCookiesSidecar_EmptyValuesAllowed(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cookies.db")
	cookies := []Cookie{
		{HostKey: ".example.com", Name: "consent_flag", Value: ""},
		{HostKey: ".example.com", Name: "with_value", Value: "actual-value"},
	}
	n, err := WriteCookiesSidecar(target, cookies, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("wrote %d, want 2", n)
	}
	db, _ := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	defer db.Close()
	var emptyCount, nonEmptyCount int
	db.QueryRow("select count(*) from cookies where value = ''").Scan(&emptyCount)
	db.QueryRow("select count(*) from cookies where value != ''").Scan(&nonEmptyCount)
	if emptyCount != 1 || nonEmptyCount != 1 {
		t.Errorf("empty/non-empty counts: %d/%d, want 1/1", emptyCount, nonEmptyCount)
	}
}

func TestWriteCookiesSidecar_DuplicateRowsTolerated(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cookies.db")
	// Same (host, name, path, scheme, port, has_cross_site_ancestor) tuple
	// twice. The ON CONFLICT clause makes the second upsert overwrite the
	// first; one row ends up in the table, second value wins.
	cookies := []Cookie{
		{HostKey: "x.com", Name: "dup", Path: "/", Value: "first"},
		{HostKey: "x.com", Name: "dup", Path: "/", Value: "second"},
	}
	if _, err := WriteCookiesSidecar(target, cookies, nil); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	defer db.Close()
	var count int
	var lastValue string
	db.QueryRow("select count(*), max(value) from cookies").Scan(&count, &lastValue)
	if count != 1 {
		t.Errorf("expected 1 deduped row, got %d", count)
	}
	if lastValue != "second" {
		t.Errorf("expected upsert to land 'second', got %q", lastValue)
	}
}
