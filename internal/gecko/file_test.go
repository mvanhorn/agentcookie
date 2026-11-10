package gecko

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/gecko/profilelock"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLZ4Bounded(t *testing.T) {
	payload := []byte(`{"cookies":[]}`)
	b := append([]byte("mozLz40\x00"), make([]byte, 4)...)
	binary.LittleEndian.PutUint32(b[8:], uint32(len(payload)))
	b = append(b, byte(len(payload)<<4))
	b = append(b, payload...)
	out, e := DecodeMozLZ4(b)
	if e != nil || string(out) != string(payload) {
		t.Fatalf("%s %v", out, e)
	}
	binary.LittleEndian.PutUint32(b[8:], maxSessionSize+1)
	if _, e = DecodeMozLZ4(b); e == nil {
		t.Fatal("unbounded allocation")
	}
}
func TestClosedFileReader(t *testing.T) {
	p := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA user_version=17;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT,schemeMap INTEGER);INSERT INTO moz_cookies VALUES('.example.com','sid','secret','/',2000000000000,1,1,256,'^userContextId=6',2)`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	before, _ := os.Stat(filepath.Join(p, "cookies.sqlite"))
	result, e := ReadFile(context.Background(), p, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(result.Cookies) != 1 || result.Cookies[0].Container != 6 || result.Cookies[0].Cookie.SameSite != -1 || result.Complete {
		t.Fatalf("%+v", result)
	}
	after, _ := os.Stat(filepath.Join(p, "cookies.sqlite"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("original changed")
	}
	l, e := profilelock.Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = ReadFile(context.Background(), p, time.Now()); !errors.Is(e, profilelock.ErrBrowserRunning) {
		t.Fatal(e)
	}
}
func TestWALRowsRecoveredOnlyInCopy(t *testing.T) {
	p := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0;PRAGMA user_version=15;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT);INSERT INTO moz_cookies VALUES('example.com','wal','value','/',2000000000,1,0,1,'')`)
	if e != nil {
		t.Fatal(e)
	}
	before, e := os.ReadFile(filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	originals := captureOriginalStores(t, p, "cookies.sqlite", "cookies.sqlite-wal", "cookies.sqlite-shm")
	// A main-file-only immutable connection must not see the WAL-only row.
	mainOnly, e := sql.Open("sqlite3", "file:"+filepath.Join(p, "cookies.sqlite")+"?mode=ro&immutable=1")
	if e != nil {
		t.Fatal(e)
	}
	var count int
	queryErr := mainOnly.QueryRow("SELECT count(*) FROM moz_cookies WHERE name='wal'").Scan(&count)
	mainOnly.Close()
	if queryErr == nil && count != 0 {
		t.Fatal("fixture row is not WAL-only")
	}
	if queryErr != nil && !strings.Contains(queryErr.Error(), "no such table") {
		t.Fatal(queryErr)
	}
	r, e := ReadFile(context.Background(), p, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	assertOriginalStoresUnchanged(t, p, originals)
	if len(r.Cookies) != 1 || r.Cookies[0].Cookie.Name != "wal" || r.Cookies[0].Cookie.ExpiresUTC != 13644473600000000 {
		t.Fatalf("%+v", r)
	}
	after, e := os.ReadFile(filepath.Join(p, "cookies.sqlite"))
	if e != nil || string(before) != string(after) {
		t.Fatal("original WAL database mutated", e)
	}
}
func TestSessionRecoveryFilesAreNeverRead(t *testing.T) {
	p := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA user_version=17;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT)`)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	os.Mkdir(filepath.Join(p, "sessionstore-backups"), 0700)
	os.WriteFile(filepath.Join(p, "sessionstore-backups", "recovery.jsonlz4"), []byte("invalid must not be opened"), 0600)
	r, e := ReadFile(context.Background(), p, time.Now())
	if e != nil || len(r.Cookies) != 0 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestHotJournalWriterHelper(t *testing.T) {
	p := os.Getenv("AGENTCOOKIE_HOT_JOURNAL_PROFILE")
	if p == "" {
		return
	}
	lock, e := profilelock.Acquire(p)
	if e != nil {
		os.Exit(2)
	}
	_ = lock
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		os.Exit(3)
	}
	if _, e = db.Exec("PRAGMA cache_size=1;PRAGMA cache_spill=ON;PRAGMA synchronous=FULL"); e != nil {
		os.Exit(4)
	}
	tx, e := db.Begin()
	if e != nil {
		os.Exit(5)
	}
	if _, e = tx.Exec("UPDATE moz_cookies SET value=?", strings.Repeat("new", 2000)); e != nil {
		os.Exit(6)
	}
	os.Exit(0)
}
func TestHotRollbackJournalRecoveredOnlyInCopy(t *testing.T) {
	p := t.TempDir()
	db, e := sql.Open("sqlite3", filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`PRAGMA journal_mode=DELETE;PRAGMA user_version=15;CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT)`)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for n := range 200 {
		if _, e = tx.Exec("INSERT INTO moz_cookies VALUES('example.com',?,?,'/',2000000000,1,0,1,'')", fmt.Sprint(n), strings.Repeat("old", 2000)); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	db.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHotJournalWriterHelper$")
	cmd.Env = append(os.Environ(), "AGENTCOOKIE_HOT_JOURNAL_PROFILE="+p)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("writer crash fixture: %v %s", e, out)
	}
	journal, e := os.ReadFile(filepath.Join(p, "cookies.sqlite-journal"))
	if e != nil || len(journal) < 28 || bytes.Equal(journal[:8], make([]byte, 8)) {
		t.Fatalf("fixture did not leave a hot journal: %v", e)
	}
	before, e := os.ReadFile(filepath.Join(p, "cookies.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	originals := captureOriginalStores(t, p, "cookies.sqlite", "cookies.sqlite-journal")
	result, e := ReadFile(context.Background(), p, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	assertOriginalStoresUnchanged(t, p, originals)
	if len(result.Cookies) != 200 {
		t.Fatal(len(result.Cookies))
	}
	for _, record := range result.Cookies {
		if record.Cookie.Value != strings.Repeat("old", 2000) {
			t.Fatal("private copy did not roll back uncommitted write")
		}
	}
	after, e := os.ReadFile(filepath.Join(p, "cookies.sqlite"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("original changed", e)
	}
	afterJournal, e := os.ReadFile(filepath.Join(p, "cookies.sqlite-journal"))
	if e != nil || !bytes.Equal(journal, afterJournal) {
		t.Fatal("original journal changed", e)
	}
}

type originalStore struct {
	bytes    []byte
	modified time.Time
}

func captureOriginalStores(t *testing.T, profile string, names ...string) map[string]originalStore {
	t.Helper()
	result := map[string]originalStore{}
	for _, name := range names {
		path := filepath.Join(profile, name)
		// Make an accidental write detectable even on coarser timestamp filesystems.
		old := time.Unix(1700000000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[name] = originalStore{data, info.ModTime()}
	}
	return result
}
func assertOriginalStoresUnchanged(t *testing.T, profile string, originals map[string]originalStore) {
	t.Helper()
	for name, original := range originals {
		path := filepath.Join(profile, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original.bytes, data) || !original.modified.Equal(info.ModTime()) {
			t.Fatalf("original %s bytes or mtime changed", name)
		}
	}
}
