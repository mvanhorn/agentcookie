package chrome

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"testing"
)

// testKey is a fixed 16-byte AES key for write/read round-trip tests.
// Real keys come from PBKDF2(SafeStoragePassword, "saltysalt", 1003, 16).
var testKey = []byte("0123456789abcdef")

// seedEmptyCookiesDB creates a Chrome-shaped Cookies SQLite at path with the
// modern (Chrome 134+) cookies table and no rows. Returns the path. Used as a
// fixture for WriteCookies tests; mirrors what Chrome creates on first launch.
func seedEmptyCookiesDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(sidecarSchema); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
}

func TestWriteCookies_PlainV10RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	in := []Cookie{
		{HostKey: ".instacart.com", Name: "_session", Value: "instacart-session-token", Path: "/", IsSecure: 1, IsPersistent: 1, HasExpires: 1, ExpiresUTC: 13380163200000000},
		{HostKey: ".github.com", Name: "user_session", Value: "github-token-xyz", Path: "/", IsSecure: 1},
	}
	n, err := WriteCookies(path, in, testKey)
	if err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}
	if n != len(in) {
		t.Fatalf("wrote %d, want %d", n, len(in))
	}

	got, err := ReadCookiesForHost(path, "%instacart%", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost(instacart): %v", err)
	}
	if len(got) != 1 || got[0].Value != "instacart-session-token" {
		t.Errorf("instacart cookie value: got %#v", got)
	}

	got2, err := ReadCookiesForHost(path, "%github.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost(github): %v", err)
	}
	if len(got2) != 1 || got2[0].Value != "github-token-xyz" {
		t.Errorf("github cookie value: got %#v", got2)
	}
}

func TestWriteCookies_NoAppBoundPrefixInPlaintext(t *testing.T) {
	// The whole point of v0.9: decrypted plaintext is the raw cookie value,
	// not SHA256(host_key) || value. PP CLIs on kooky v0.2.2 read this file
	// without stripping the prefix; if we ship the prefix here, they get
	// garbage.
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	host := ".instacart.com"
	value := "session-token-payload"
	if _, err := WriteCookies(path, []Cookie{{HostKey: host, Name: "n", Value: value, Path: "/"}}, testKey); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}

	// Read the raw encrypted_value blob, decrypt without the App-Bound
	// strip, and assert the result is exactly the value (not 32-bytes-of-
	// SHA + value).
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var enc []byte
	if err := db.QueryRow(`SELECT encrypted_value FROM cookies WHERE host_key = ?`, host).Scan(&enc); err != nil {
		t.Fatalf("scan encrypted: %v", err)
	}
	plain, err := decryptValue(enc, testKey)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != value {
		t.Errorf("plaintext: got %q, want %q (likely still carries App-Bound prefix)", plain, value)
	}

	// Stronger: assert SHA256(host) is not the first 32 bytes.
	hostHash := sha256.Sum256([]byte(host))
	if len(plain) >= sha256.Size && bytes.Equal([]byte(plain)[:sha256.Size], hostHash[:]) {
		t.Errorf("decrypted plaintext starts with SHA256(host_key); App-Bound prefix is still being emitted")
	}
}

func TestWriteCookies_EmptyValueRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	if _, err := WriteCookies(path, []Cookie{{HostKey: ".example.com", Name: "consent", Value: "", Path: "/"}}, testKey); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}
	got, err := ReadCookiesForHost(path, "%example.com", testKey)
	if err != nil {
		t.Fatalf("ReadCookiesForHost: %v", err)
	}
	if len(got) != 1 || got[0].Value != "" {
		t.Errorf("empty value round-trip: got %#v", got)
	}
}

func TestWriteCookies_MetaVersion18Written(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	if _, err := WriteCookies(path, []Cookie{{HostKey: ".a.com", Name: "n", Value: "v", Path: "/"}}, testKey); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}

	db, _ := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read meta.version: %v", err)
	}
	if version != "18" {
		t.Errorf("meta.version: got %q, want 18", version)
	}
}

func TestWriteCookies_MetaVersionOverwritesExisting(t *testing.T) {
	// Simulate the realistic case: Chrome 134+ created the file with
	// meta.version=24. agentcookie's write should pin it back to 18.
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	// Pre-seed meta with version=24 the way Chrome 134+ would.
	db, _ := sql.Open("sqlite3", "file:"+path)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR)`); err != nil {
		t.Fatalf("pre-seed meta table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES ('version', '24')`); err != nil {
		t.Fatalf("pre-seed meta row: %v", err)
	}
	db.Close()

	if _, err := WriteCookies(path, []Cookie{{HostKey: ".a.com", Name: "n", Value: "v", Path: "/"}}, testKey); err != nil {
		t.Fatalf("WriteCookies: %v", err)
	}

	db2, _ := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	defer db2.Close()
	var version string
	if err := db2.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read meta.version: %v", err)
	}
	if version != "18" {
		t.Errorf("meta.version after overwrite: got %q, want 18", version)
	}
	var count int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM meta WHERE key='version'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 meta.version row, got %d (UPSERT created a duplicate)", count)
	}
}

func TestWriteCookies_MetaVersionIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)

	for i := range 3 {
		if _, err := WriteCookies(path, []Cookie{{HostKey: ".a.com", Name: "n", Value: "v", Path: "/"}}, testKey); err != nil {
			t.Fatalf("WriteCookies pass %d: %v", i, err)
		}
	}

	db, _ := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	defer db.Close()
	var version string
	var rowCount int
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&version); err != nil {
		t.Fatalf("read meta.version: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&rowCount); err != nil {
		t.Fatalf("count meta rows: %v", err)
	}
	if version != "18" || rowCount != 1 {
		t.Errorf("idempotency: version=%q (want 18) rowCount=%d (want 1)", version, rowCount)
	}
}
