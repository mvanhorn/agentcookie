package gecko

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// The v17 DDL below is copied from a real Firefox profile.
const v17Schema = `CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, originAttributes TEXT NOT NULL DEFAULT '', name TEXT, value TEXT, host TEXT, path TEXT, expiry INTEGER, lastAccessed INTEGER, creationTime INTEGER, isSecure INTEGER, isHttpOnly INTEGER, inBrowserElement INTEGER DEFAULT 0, sameSite INTEGER DEFAULT 0, schemeMap INTEGER DEFAULT 0, isPartitionedAttributeSet INTEGER DEFAULT 0, updateTime INTEGER, CONSTRAINT moz_uniqueid UNIQUE (name, host, path, originAttributes))`

// v14 is the last schema with rawSameSite and second-resolution expiry.
const v14Schema = `CREATE TABLE moz_cookies (id INTEGER PRIMARY KEY, originAttributes TEXT NOT NULL DEFAULT '', name TEXT, value TEXT, host TEXT, path TEXT, expiry INTEGER, lastAccessed INTEGER, creationTime INTEGER, isSecure INTEGER, isHttpOnly INTEGER, inBrowserElement INTEGER DEFAULT 0, sameSite INTEGER DEFAULT 0, rawSameSite INTEGER DEFAULT 0, schemeMap INTEGER DEFAULT 0, isPartitionedAttributeSet INTEGER DEFAULT 0, CONSTRAINT moz_uniqueid UNIQUE (name, host, path, originAttributes))`

// Timing values copied from a real v17 row: expiry in epoch milliseconds,
// lastAccessed in epoch microseconds.
const (
	realExpiryMS       int64 = 1807221037000
	realLastAccessedUS int64 = 1775685037686703
)

var fixtureNow = time.Unix(1790000000, 0).UTC()

// Chrome stores times as microseconds since 1601-01-01.
func chromeMicros(unixSec int64) int64 { return (unixSec + 11644473600) * 1_000_000 }

type mozRow struct {
	originAttributes string
	name, host, path string
	expiry           int64
	isSecure         int
	isHTTPOnly       int
	sameSite         int
	rawSameSite      int
	schemeMap        int
}

func row(name, host string, expiry int64) mozRow {
	return mozRow{name: name, host: host, path: "/", expiry: expiry, sameSite: 256, schemeMap: 2}
}

// makeMozCookies builds a WAL-mode cookies.sqlite at dir/cookies.sqlite and
// returns the open handle so a caller can keep it open like a running
// browser. The caller closes it, which checkpoints and removes the WAL.
func makeMozCookies(t *testing.T, dir string, version int, rows []mozRow) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, CookiesFile)
	db, err := sql.Open("sqlite3", "file:"+path+"?_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	schema := v17Schema
	if version < 15 {
		schema = v14Schema
	}
	for _, stmt := range []string{schema, "PRAGMA user_version = " + strconv.Itoa(version)} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	insertRows(t, db, version, rows)
	return db
}

func insertRows(t *testing.T, db *sql.DB, version int, rows []mozRow) {
	t.Helper()
	for _, r := range rows {
		var err error
		if version < 15 {
			_, err = db.Exec(`INSERT INTO moz_cookies (originAttributes, name, value, host, path, expiry, lastAccessed, creationTime, isSecure, isHttpOnly, sameSite, rawSameSite, schemeMap) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				r.originAttributes, r.name, "v-"+r.name, r.host, r.path, r.expiry, realLastAccessedUS, realLastAccessedUS, r.isSecure, r.isHTTPOnly, r.sameSite, r.rawSameSite, r.schemeMap)
		} else {
			_, err = db.Exec(`INSERT INTO moz_cookies (originAttributes, name, value, host, path, expiry, lastAccessed, creationTime, isSecure, isHttpOnly, sameSite, schemeMap, updateTime) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				r.originAttributes, r.name, "v-"+r.name, r.host, r.path, r.expiry, realLastAccessedUS, realLastAccessedUS, r.isSecure, r.isHTTPOnly, r.sameSite, r.schemeMap, realLastAccessedUS)
		}
		if err != nil {
			t.Fatalf("insert %s: %v", r.name, err)
		}
	}
}

// writeFixture builds a closed fixture (no WAL left behind) and returns its path.
func writeFixture(t *testing.T, version int, rows []mozRow) string {
	t.Helper()
	dir := t.TempDir()
	db := makeMozCookies(t, dir, version, rows)
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	return filepath.Join(dir, CookiesFile)
}

func byName(t *testing.T, cookies []chrome.Cookie) map[string]chrome.Cookie {
	t.Helper()
	m := make(map[string]chrome.Cookie, len(cookies))
	for _, c := range cookies {
		m[c.Name] = c
	}
	return m
}

func TestReadCookiesCurrentSchemaMapsDomainHostOnlyAndDropsExpired(t *testing.T) {
	path := writeFixture(t, 17, []mozRow{
		row("domain", ".example.com", realExpiryMS),
		row("hostonly", "www.example.com", realExpiryMS),
		row("expired", ".example.com", fixtureNow.Add(-time.Hour).UnixMilli()),
	})
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	m := byName(t, got)
	if len(got) != 2 {
		t.Fatalf("got %d cookies %v, want domain and hostonly", len(got), m)
	}
	if _, ok := m["expired"]; ok {
		t.Error("expired cookie was not dropped")
	}
	wantExpires := chromeMicros(realExpiryMS / 1000)
	wantLast := realLastAccessedUS + 11644473600*1_000_000
	for name, host := range map[string]string{"domain": ".example.com", "hostonly": "www.example.com"} {
		c, ok := m[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if c.HostKey != host || c.Path != "/" || c.Value != "v-"+name {
			t.Errorf("%s: host/path/value = %q %q %q", name, c.HostKey, c.Path, c.Value)
		}
		if c.ExpiresUTC != wantExpires {
			t.Errorf("%s: ExpiresUTC = %d, want %d", name, c.ExpiresUTC, wantExpires)
		}
		if c.LastAccessUTC != wantLast {
			t.Errorf("%s: LastAccessUTC = %d, want %d", name, c.LastAccessUTC, wantLast)
		}
		if c.HasExpires != 1 || c.IsPersistent != 1 {
			t.Errorf("%s: HasExpires/IsPersistent = %d/%d, want 1/1", name, c.HasExpires, c.IsPersistent)
		}
		if c.SourcePort != -1 || c.Priority != 1 {
			t.Errorf("%s: SourcePort/Priority = %d/%d, want -1/1", name, c.SourcePort, c.Priority)
		}
		if c.TopFrameSiteKey != "" || c.HasCrossSiteAncestor != 0 {
			t.Errorf("%s: partition fields set: %q %d", name, c.TopFrameSiteKey, c.HasCrossSiteAncestor)
		}
	}
}

func TestReadCookiesLegacySchemaGivesSameChromeExpiry(t *testing.T) {
	legacy := writeFixture(t, 14, []mozRow{row("domain", ".example.com", realExpiryMS/1000)})
	current := writeFixture(t, 17, []mozRow{row("domain", ".example.com", realExpiryMS)})
	l, err := ReadCookies(legacy, fixtureNow)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	c, err := ReadCookies(current, fixtureNow)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if len(l) != 1 || len(c) != 1 {
		t.Fatalf("got %d legacy and %d current cookies, want 1 each", len(l), len(c))
	}
	if l[0].ExpiresUTC != c[0].ExpiresUTC || l[0].ExpiresUTC != chromeMicros(realExpiryMS/1000) {
		t.Errorf("legacy ExpiresUTC %d, current %d, want both %d", l[0].ExpiresUTC, c[0].ExpiresUTC, chromeMicros(realExpiryMS/1000))
	}
}

func TestReadCookiesLegacyExpiredDropped(t *testing.T) {
	path := writeFixture(t, 14, []mozRow{
		row("old", ".example.com", fixtureNow.Add(-time.Minute).Unix()),
		row("live", ".example.com", fixtureNow.Add(time.Minute).Unix()),
	})
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	if len(got) != 1 || got[0].Name != "live" {
		t.Errorf("got %v, want only the unexpired cookie", byName(t, got))
	}
}

func TestReadCookiesSecureAndHTTPOnly(t *testing.T) {
	both := row("both", ".example.com", realExpiryMS)
	both.isSecure, both.isHTTPOnly = 1, 1
	path := writeFixture(t, 17, []mozRow{both, row("neither", ".example.com", realExpiryMS)})
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	m := byName(t, got)
	if c := m["both"]; c.IsSecure != 1 || c.IsHTTPOnly != 1 {
		t.Errorf("both: IsSecure/IsHTTPOnly = %d/%d, want 1/1", c.IsSecure, c.IsHTTPOnly)
	}
	if c := m["neither"]; c.IsSecure != 0 || c.IsHTTPOnly != 0 {
		t.Errorf("neither: IsSecure/IsHTTPOnly = %d/%d, want 0/0", c.IsSecure, c.IsHTTPOnly)
	}
}

func TestReadCookiesSkipsContainerPrivateAndPartitionedRows(t *testing.T) {
	container := row("container", ".example.com", realExpiryMS)
	container.originAttributes = "^userContextId=2"
	private := row("private", ".example.com", realExpiryMS)
	private.originAttributes = "^privateBrowsingId=1"
	partitioned := row("partitioned", ".example.com", realExpiryMS)
	partitioned.originAttributes = "^partitionKey=%28https%2Cexample.org%29"
	path := writeFixture(t, 17, []mozRow{container, private, partitioned, row("plain", ".example.com", realExpiryMS)})
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	if len(got) != 1 || got[0].Name != "plain" {
		t.Errorf("got %v, want only the plain cookie", byName(t, got))
	}
}

func TestReadCookiesSameSiteAndScheme(t *testing.T) {
	t.Run("current schema", func(t *testing.T) {
		var rows []mozRow
		for name, ss := range map[string]int{"none": 0, "lax": 1, "strict": 2, "unset": 256} {
			r := row(name, ".example.com", realExpiryMS)
			r.sameSite = ss
			if ss == 0 {
				r.isSecure = 1 // SameSite=None only holds with Secure
			}
			rows = append(rows, r)
		}
		httpOnly := row("http", ".example.com", realExpiryMS)
		httpOnly.schemeMap = 1
		rows = append(rows, httpOnly)
		path := writeFixture(t, 17, rows)
		got, err := ReadCookies(path, fixtureNow)
		if err != nil {
			t.Fatalf("ReadCookies: %v", err)
		}
		m := byName(t, got)
		for name, want := range map[string]int{"none": 0, "lax": 1, "strict": 2, "unset": -1} {
			if m[name].SameSite != want {
				t.Errorf("%s: SameSite = %d, want %d", name, m[name].SameSite, want)
			}
		}
		if m["lax"].SourceScheme != 2 {
			t.Errorf("https schemeMap: SourceScheme = %d, want 2", m["lax"].SourceScheme)
		}
		if m["http"].SourceScheme != 1 {
			t.Errorf("http schemeMap: SourceScheme = %d, want 1", m["http"].SourceScheme)
		}
	})

	t.Run("legacy schema", func(t *testing.T) {
		mk := func(name string, ss, raw int) mozRow {
			r := row(name, ".example.com", realExpiryMS/1000)
			r.sameSite, r.rawSameSite = ss, raw
			return r
		}
		none := mk("none", 0, 0)
		none.isSecure = 1 // SameSite=None only holds with Secure
		path := writeFixture(t, 14, []mozRow{
			none,
			mk("lax", 1, 1),
			mk("strict", 2, 2),
			// Lax applied by default to a cookie that sent no SameSite.
			mk("unset", 1, 0),
		})
		got, err := ReadCookies(path, fixtureNow)
		if err != nil {
			t.Fatalf("ReadCookies: %v", err)
		}
		m := byName(t, got)
		for name, want := range map[string]int{"none": 0, "lax": 1, "strict": 2, "unset": -1} {
			if m[name].SameSite != want {
				t.Errorf("%s: SameSite = %d, want %d", name, m[name].SameSite, want)
			}
		}
	})
}

type fileState struct {
	data  []byte
	mtime time.Time
}

func snapshot(t *testing.T, path string) fileState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fileState{data: data, mtime: fi.ModTime()}
}

func TestReadCookiesReadsWALRowsWithoutTouchingLiveFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := t.TempDir()
	db := makeMozCookies(t, dir, 17, nil)
	defer db.Close()
	// Checkpoint the table into the main file, then stop checkpointing so
	// the next insert stays in the WAL.
	for _, stmt := range []string{"PRAGMA wal_checkpoint(TRUNCATE)", "PRAGMA wal_autocheckpoint = 0"} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	insertRows(t, db, 17, []mozRow{row("walonly", ".example.com", realExpiryMS)})

	path := filepath.Join(dir, CookiesFile)
	wal := path + "-wal"
	// The row must live only in the WAL: the main file alone has none.
	mainOnly, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := mainOnly.QueryRow("SELECT count(*) FROM moz_cookies").Scan(&n); err != nil {
		t.Fatal(err)
	}
	mainOnly.Close()
	if n != 0 {
		t.Fatalf("fixture main file already holds %d rows; the test needs a WAL-only row", n)
	}

	beforeDB, beforeWAL := snapshot(t, path), snapshot(t, wal)
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	if len(got) != 1 || got[0].Name != "walonly" {
		t.Fatalf("got %v, want the WAL-only row", byName(t, got))
	}
	for _, c := range []struct {
		path   string
		before fileState
	}{{path, beforeDB}, {wal, beforeWAL}} {
		after := snapshot(t, c.path)
		if !bytes.Equal(after.data, c.before.data) || !after.mtime.Equal(c.before.mtime) {
			t.Errorf("%s changed during the read", filepath.Base(c.path))
		}
	}
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temp copy left behind: %v", left)
	}
}

func TestReadCookiesWithoutWALFile(t *testing.T) {
	path := writeFixture(t, 17, []mozRow{row("closed", ".example.com", realExpiryMS)})
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("fixture still has a -wal file (err=%v)", err)
	}
	got, err := ReadCookies(path, fixtureNow)
	if err != nil {
		t.Fatalf("ReadCookies: %v", err)
	}
	if len(got) != 1 || got[0].Name != "closed" {
		t.Errorf("got %v, want the one cookie", byName(t, got))
	}
}

func TestReadCookiesFailsWhenLiveFileNeverSettles(t *testing.T) {
	path := writeFixture(t, 17, []mozRow{row("a", ".example.com", realExpiryMS)})
	stamp := time.Now()
	afterCopy = func() {
		stamp = stamp.Add(time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Errorf("chtimes: %v", err)
		}
	}
	t.Cleanup(func() { afterCopy = nil })
	got, err := ReadCookies(path, fixtureNow)
	if err == nil {
		t.Fatal("expected an error when the live file changes during every copy")
	}
	if got != nil {
		t.Errorf("got %d cookies with the error, want none", len(got))
	}
}
