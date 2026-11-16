package chrome

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// ReadCookieValue decrypts only the named cookie, so a host's other cookies
// (a session key, say) are never decrypted and cannot fail the read.
func TestReadCookieValue_DecryptsOnlyTheNamedCookie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)
	in := []Cookie{
		{HostKey: ".claude.ai", Name: "lastActiveOrg", Value: "adce0660-6e46-4b48-996d-ad7059e69d99", Path: "/"},
		{HostKey: ".claude.ai", Name: "sessionKey", Value: "secret", Path: "/"},
	}
	if _, err := WriteCookies(path, in, testKey); err != nil {
		t.Fatal(err)
	}
	// Corrupt sessionKey so decrypting it would fail.
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cookies SET encrypted_value = X'763130DEADBEEF' WHERE name = 'sessionKey'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := ReadCookiesForHost(path, "%claude.ai", testKey); err == nil {
		t.Fatal("precondition: reading every claude.ai cookie should fail on the corrupted row")
	}

	got, ok, err := ReadCookieValue(path, "%claude.ai", "lastActiveOrg", testKey)
	if err != nil || !ok || got != "adce0660-6e46-4b48-996d-ad7059e69d99" {
		t.Fatalf("got %q ok=%v err=%v", got, ok, err)
	}
}

func TestReadCookieValue_Missing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	seedEmptyCookiesDB(t, path)
	_, ok, err := ReadCookieValue(path, "%claude.ai", "lastActiveOrg", testKey)
	if err != nil || ok {
		t.Fatalf("missing cookie: ok=%v err=%v, want false nil", ok, err)
	}
}
