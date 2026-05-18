package chrome

import (
	"crypto/rand"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteCookiesSidecar_SealedValues exercises the v0.12 path: when
// a master key is passed, the value column stores SealedPrefix + base64
// instead of raw plaintext. A grep for the known cookie value would
// return zero matches.
func TestWriteCookiesSidecar_SealedValues(t *testing.T) {
	masterKey := make([]byte, 32)
	_, _ = rand.Read(masterKey)

	target := filepath.Join(t.TempDir(), "cookies-plain.db")
	cookies := []Cookie{
		{HostKey: "instacart.com", Name: "session", Value: "secretvalue123", Path: "/"},
		{HostKey: ".instacart.com", Name: "_ga", Value: "GA1.2.456", Path: "/"},
	}
	n, err := WriteCookiesSidecar(target, cookies, masterKey)
	if err != nil {
		t.Fatalf("WriteCookiesSidecar: %v", err)
	}
	if n != len(cookies) {
		t.Errorf("wrote %d cookies, want %d", n, len(cookies))
	}

	db, _ := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	defer db.Close()
	rows, _ := db.Query("select host_key, value from cookies order by host_key")
	defer rows.Close()
	for rows.Next() {
		var h, v string
		_ = rows.Scan(&h, &v)
		if !strings.HasPrefix(v, SidecarSealedPrefix) {
			t.Errorf("host %s value not prefixed with %q: got %q", h, SidecarSealedPrefix, v)
		}
		if strings.Contains(v, "secretvalue123") || strings.Contains(v, "GA1.2.456") {
			t.Errorf("host %s value column leaks plaintext: %q", h, v)
		}
	}
}

// TestWriteCookiesSidecar_NilMasterKeyPlaintext covers the legacy
// (v0.11) path: when masterKey is nil, the value column is plaintext.
func TestWriteCookiesSidecar_NilMasterKeyPlaintext(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cookies-plain.db")
	cookies := []Cookie{
		{HostKey: "instacart.com", Name: "session", Value: "plaintext123", Path: "/"},
	}
	if _, err := WriteCookiesSidecar(target, cookies, nil); err != nil {
		t.Fatalf("WriteCookiesSidecar: %v", err)
	}

	db, _ := sql.Open("sqlite3", "file:"+target+"?mode=ro")
	defer db.Close()
	rows, _ := db.Query("select value from cookies")
	defer rows.Close()
	for rows.Next() {
		var v string
		_ = rows.Scan(&v)
		if v != "plaintext123" {
			t.Errorf("expected plaintext value, got %q", v)
		}
	}
}
