package chrome

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeBoundSessionStore(t *testing.T, dir string, keys ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(dir, boundSessionStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE dbsc_session_tbl (key TEXT, proto BLOB, PRIMARY KEY(key))`); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := db.Exec(`INSERT INTO dbsc_session_tbl (key, proto) VALUES (?, x'00')`, k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBoundSessionSites_ListsDistinctSites(t *testing.T) {
	dir := t.TempDir()
	// Chrome keys the table by schemeful site, one row per site.
	writeBoundSessionStore(t, dir, "https://claude.ai", "https://google.com", "https://bilt.com", "http://bilt.com", "")
	got, err := BoundSessionSites(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bilt.com", "claude.ai", "google.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sites = %v, want %v", got, want)
	}
}

func TestBoundSessionSites_MissingStoreIsEmpty(t *testing.T) {
	got, err := BoundSessionSites(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty and no error", got, err)
	}
}

func TestBoundSessionSites_UnreadableStoreIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, boundSessionStoreFile), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BoundSessionSites(dir); err == nil {
		t.Fatal("want an error for a store that exists but cannot be read")
	}

	noTable := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(noTable, boundSessionStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := BoundSessionSites(noTable); err == nil {
		t.Fatal("want an error for a store without the sessions table")
	}
}
