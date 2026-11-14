package gecko

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

const (
	// Chrome stores times as microseconds since 1601-01-01.
	chromeEpochOffsetSec = 11644473600

	// moz_cookies schema versions (PRAGMA user_version) where the meaning of
	// a column changed. From Mozilla's CookiePersistentStorage.cpp upgrade
	// steps: the step out of version 14 drops rawSameSite and introduces
	// sameSite 256 for "unset"; the step out of version 15 multiplies expiry
	// by 1000, so from version 16 expiry is in epoch milliseconds.
	schemaNoRawSameSite = 15
	schemaExpiryMillis  = 16
	// schemeMap, which carries the cookie's source scheme, arrives in
	// version 12. Older files are not read.
	schemaMinSupported = 12

	// Gecko sameSite values (nsICookie). 256 is "unset" from version 15.
	mozSameSiteNone   = 0
	mozSameSiteLax    = 1
	mozSameSiteStrict = 2

	// Gecko schemeMap bits.
	mozSchemeHTTP  = 0x01
	mozSchemeHTTPS = 0x02

	copyAttempts = 5
	copyBackoff  = 50 * time.Millisecond
)

// afterCopy, when set, runs after each copy attempt and before the live
// files are checked again. Tests use it to simulate a browser write.
var afterCopy func()

// ReadCookies returns the ordinary cookies in the Gecko cookies.sqlite at
// path. It copies the file and its -wal into a private temp directory, reads
// the copy, and deletes it, so the live profile is never opened and the read
// works while the browser runs. Container, private-browsing and partitioned
// cookies, and cookies already expired at now, are left out.
//
// The read is all or nothing: if the live files keep changing during the
// copy, or the copy does not read cleanly, it returns an error and no
// cookies.
func ReadCookies(path string, now time.Time) ([]chrome.Cookie, error) {
	tmp, err := os.MkdirTemp("", "agentcookie-gecko-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for %s copy: %w", CookiesFile, err)
	}
	defer os.RemoveAll(tmp)

	cp, err := copyStable(path, tmp)
	if err != nil {
		return nil, err
	}
	return readCopy(cp, now)
}

type liveState struct {
	exists bool
	size   int64
	mtime  time.Time
}

func statLive(path string) (liveState, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return liveState{}, nil
	}
	if err != nil {
		return liveState{}, err
	}
	return liveState{exists: true, size: fi.Size(), mtime: fi.ModTime()}, nil
}

// copyStable copies path and path-wal into dir and returns the copy's path.
// A copy only counts when neither live file changed size or mtime while it
// was taken; otherwise it retries a few times and then gives up.
func copyStable(path, dir string) (string, error) {
	dst := filepath.Join(dir, CookiesFile)
	live := []string{path, path + "-wal"}
	for attempt := range copyAttempts {
		if attempt > 0 {
			time.Sleep(copyBackoff)
		}
		before := make([]liveState, len(live))
		for i, p := range live {
			st, err := statLive(p)
			if err != nil {
				return "", fmt.Errorf("stat %s: %w", p, err)
			}
			before[i] = st
		}
		if !before[0].exists {
			return "", fmt.Errorf("%s not found at %s", CookiesFile, path)
		}
		if err := copyFile(path, dst); err != nil {
			return "", err
		}
		if before[1].exists {
			if err := copyFile(live[1], dst+"-wal"); err != nil {
				return "", err
			}
		} else if err := os.WriteFile(dst+"-wal", nil, 0o600); err != nil {
			// SQLite will not open a WAL-mode file read-only without a -wal
			// file beside it, which a cleanly closed profile does not have.
			return "", fmt.Errorf("create empty -wal for %s copy: %w", CookiesFile, err)
		}
		if afterCopy != nil {
			afterCopy()
		}
		settled := true
		for i, p := range live {
			st, err := statLive(p)
			if err != nil {
				return "", fmt.Errorf("stat %s: %w", p, err)
			}
			if st != before[i] {
				settled = false
			}
		}
		if settled {
			return dst, nil
		}
	}
	return "", fmt.Errorf("%s kept changing while it was copied (%d attempts); the browser is writing it, try again", path, copyAttempts)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return nil
}

type mozCookie struct {
	originAttributes string
	name, value      string
	host, path       string
	expiry           int64
	lastAccessed     int64
	isSecure         int
	isHTTPOnly       int
	sameSite         int
	rawSameSite      sql.NullInt64
	schemeMap        int
}

func readCopy(path string, now time.Time) ([]chrome.Cookie, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open %s copy: %w", CookiesFile, err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, fmt.Errorf("read %s schema version: %w", CookiesFile, err)
	}
	if version < schemaMinSupported {
		return nil, fmt.Errorf("%s schema version %d is older than %d, the oldest supported; open the profile in a current browser once to upgrade it", CookiesFile, version, schemaMinSupported)
	}

	rawSameSite := "rawSameSite"
	if version >= schemaNoRawSameSite {
		rawSameSite = "NULL"
	}
	// The columns are nullable in the schema; COALESCE keeps one odd row
	// from failing the whole read.
	rows, err := db.Query(`SELECT originAttributes, COALESCE(name, ''), COALESCE(value, ''),
		COALESCE(host, ''), COALESCE(path, ''), COALESCE(expiry, 0), COALESCE(lastAccessed, 0),
		COALESCE(isSecure, 0), COALESCE(isHttpOnly, 0), COALESCE(sameSite, 0), ` + rawSameSite + `,
		COALESCE(schemeMap, 0) FROM moz_cookies`)
	if err != nil {
		return nil, fmt.Errorf("read moz_cookies: %w", err)
	}
	defer rows.Close()

	var out []chrome.Cookie
	for rows.Next() {
		var r mozCookie
		if err := rows.Scan(&r.originAttributes, &r.name, &r.value, &r.host, &r.path, &r.expiry, &r.lastAccessed, &r.isSecure, &r.isHTTPOnly, &r.sameSite, &r.rawSameSite, &r.schemeMap); err != nil {
			return nil, fmt.Errorf("read moz_cookies row: %w", err)
		}
		if c, ok := mapCookie(r, version, now); ok {
			out = append(out, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read moz_cookies: %w", err)
	}
	return out, nil
}

// mapCookie converts one moz_cookies row into a Chrome cookie record with
// the same meaning downstream. It reports false for rows that are skipped:
// any non-default originAttributes (containers, private browsing,
// partitioned cookies) and cookies already expired at now.
func mapCookie(r mozCookie, version int, now time.Time) (chrome.Cookie, bool) {
	if r.originAttributes != "" {
		return chrome.Cookie{}, false
	}
	c := chrome.Cookie{
		HostKey:      r.host,
		Name:         r.name,
		Value:        r.value,
		Path:         r.path,
		IsSecure:     r.isSecure,
		IsHTTPOnly:   r.isHTTPOnly,
		Priority:     1, // medium
		SameSite:     mapSameSite(r, version),
		SourceScheme: mapScheme(r.schemeMap),
		SourcePort:   -1, // Gecko stores no port; -1 is Chrome's "unspecified"
	}
	if r.expiry > 0 {
		expSec := r.expiry
		if version >= schemaExpiryMillis {
			expSec = r.expiry / 1000
		}
		if expSec <= now.Unix() {
			return chrome.Cookie{}, false
		}
		c.ExpiresUTC = (expSec + chromeEpochOffsetSec) * 1_000_000
		c.HasExpires = 1
		c.IsPersistent = 1
	}
	if r.lastAccessed > 0 {
		// lastAccessed is microseconds since 1970 in every schema.
		c.LastAccessUTC = r.lastAccessed + chromeEpochOffsetSec*1_000_000
	}
	return c, true
}

// mapSameSite returns Chrome's SameSite value: -1 unspecified, 0 None,
// 1 Lax, 2 Strict.
func mapSameSite(r mozCookie, version int) int {
	if version < schemaNoRawSameSite && r.sameSite == mozSameSiteLax &&
		r.rawSameSite.Valid && r.rawSameSite.Int64 == mozSameSiteNone {
		// Older schemas wrote Lax-by-default into sameSite for a cookie
		// that sent no SameSite, keeping the sent value in rawSameSite.
		// Mozilla's own upgrade turns exactly these rows into "unset".
		return -1
	}
	switch r.sameSite {
	case mozSameSiteNone:
		return 0
	case mozSameSiteLax:
		return 1
	case mozSameSiteStrict:
		return 2
	default: // 256 (unset), or a value this reader does not know
		return -1
	}
}

// mapScheme returns Chrome's source scheme: 2 secure, 1 non-secure,
// 0 unset.
func mapScheme(schemeMap int) int {
	switch {
	case schemeMap&mozSchemeHTTPS != 0:
		return 2
	case schemeMap&mozSchemeHTTP != 0:
		return 1
	default:
		return 0
	}
}
