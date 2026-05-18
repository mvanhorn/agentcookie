package chrome

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// SidecarSealedPrefix mirrors pkg/sidecar.SealedPrefix. Duplicated here
// to avoid an import cycle: pkg/sidecar imports internal/keystore, and
// this file already imports internal/keystore. internal/chrome can't
// import pkg/sidecar without flipping the cycle. The two constants
// are kept in sync by test.
const SidecarSealedPrefix = "agc1:"

// sealSidecarValue produces the on-disk form of a value-column entry
// sealed under masterKey.
func sealSidecarValue(masterKey []byte, plaintext string) (string, error) {
	env, err := keystore.Seal(masterKey, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return SidecarSealedPrefix + base64.StdEncoding.EncodeToString(env), nil
}

// sidecarSchema is the cookies-table schema we create in the sidecar SQLite.
// Matches Chrome's modern (134+) schema exactly so kooky and other Chrome-
// cookie libraries read it as if it were Chrome's own Cookies file. The
// `value` column is text (plaintext); `encrypted_value` is a zero-byte
// blob. Libraries that check `encrypted_value` first and fall back to
// `value` when empty (kooky, lite-chrome, others) work transparently.
const sidecarSchema = `
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

// WriteCookiesSidecar writes cookies into a Chrome-shaped SQLite at
// targetPath. v0.11 wrote the `value` column as raw plaintext; v0.12
// optionally seals each value under the agentcookie master key. The
// helper is invoked by agentcookie sink and produces a file that
// kooky-style libraries (PP CLIs) can open via pkg/sidecar.
//
// When masterKey is nil, behaviour is identical to v0.11: each value
// is stored as plaintext UTF-8. When masterKey is non-nil (32 bytes,
// the keystore master key), each value is replaced with
// pkg/sidecar.SealedPrefix + base64(seal(masterKey, plaintext)).
// Downstream readers either auto-detect via the prefix or use the
// pkg/sidecar.ReadSidecar reader.
//
// The write is atomic: temp file in the same directory, renamed into
// place once the transaction commits. Readers either see the previous
// file or the new file, never a half-state.
//
// Returns the number of cookies written.
func WriteCookiesSidecar(targetPath string, cookies []Cookie, masterKey []byte) (int, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		return 0, fmt.Errorf("mkdir sidecar parent: %w", err)
	}
	tmpPath := targetPath + ".agentcookie.tmp"
	_ = os.Remove(tmpPath)

	db, err := sql.Open("sqlite3", "file:"+tmpPath+"?_journal=WAL")
	if err != nil {
		return 0, fmt.Errorf("open sidecar tmp: %w", err)
	}
	if _, err := db.Exec(sidecarSchema); err != nil {
		db.Close()
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("create sidecar schema: %w", err)
	}

	cols := sidecarColumnSet()
	insertSQL, args := buildUpsert(cols)

	tx, err := db.Begin()
	if err != nil {
		db.Close()
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("begin sidecar tx: %w", err)
	}
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		tx.Rollback()
		db.Close()
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("prepare sidecar upsert: %w", err)
	}

	nowChrome := time.Now().UnixMicro() + chromeEpochDeltaMicros
	written := 0
	for _, c := range cookies {
		row := args(rowInput{
			cookie:        c,
			encrypted:     []byte{},
			creationUTC:   nowChrome,
			lastUpdateUTC: nowChrome,
		})
		valueOnDisk := c.Value
		if len(masterKey) > 0 {
			sealed, err := sealSidecarValue(masterKey, c.Value)
			if err != nil {
				stmt.Close()
				tx.Rollback()
				db.Close()
				_ = os.Remove(tmpPath)
				return written, fmt.Errorf("seal sidecar value %s/%s: %w", c.HostKey, c.Name, err)
			}
			valueOnDisk = sealed
		}
		patchPlaintextValue(row, cols, valueOnDisk)
		if _, err := stmt.Exec(row...); err != nil {
			if isUniqueConstraintError(err) {
				continue
			}
			stmt.Close()
			tx.Rollback()
			db.Close()
			_ = os.Remove(tmpPath)
			return written, fmt.Errorf("upsert sidecar %s/%s: %w", c.HostKey, c.Name, err)
		}
		written++
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		db.Close()
		_ = os.Remove(tmpPath)
		return written, fmt.Errorf("commit sidecar tx: %w", err)
	}
	if err := db.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return written, fmt.Errorf("close sidecar db: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return written, fmt.Errorf("chmod sidecar: %w", err)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return written, fmt.Errorf("rename sidecar into place: %w", err)
	}
	return written, nil
}

// sidecarColumnSet returns the column set buildUpsert needs. Sidecar
// always uses the full modern Chrome schema (we control the table).
func sidecarColumnSet() map[string]bool {
	return map[string]bool{
		"creation_utc": true, "host_key": true, "top_frame_site_key": true,
		"name": true, "value": true, "encrypted_value": true, "path": true,
		"expires_utc": true, "is_secure": true, "is_httponly": true,
		"last_access_utc": true, "has_expires": true, "is_persistent": true,
		"priority": true, "samesite": true, "source_scheme": true,
		"source_port": true, "last_update_utc": true, "source_type": true,
		"has_cross_site_ancestor": true,
	}
}

// patchPlaintextValue overwrites the `value` column slot with the plaintext
// cookie value before stmt.Exec. buildUpsert's default `get` returns ""
// for `value` (matching Chrome's encrypted-mode behavior). For the sidecar
// we want the actual value there.
func patchPlaintextValue(row []any, cols map[string]bool, plaintext string) {
	idx := 0
	for _, name := range orderedSidecarColumns() {
		if !cols[name] {
			continue
		}
		if name == "value" {
			row[idx] = plaintext
			return
		}
		idx++
	}
}

// orderedSidecarColumns must match the candidate order in buildUpsert
// so positional indexing into the row slice lines up.
func orderedSidecarColumns() []string {
	return []string{
		"creation_utc", "host_key", "top_frame_site_key", "name", "value",
		"encrypted_value", "path", "expires_utc", "is_secure", "is_httponly",
		"last_access_utc", "has_expires", "is_persistent", "priority",
		"samesite", "source_scheme", "source_port", "last_update_utc",
		"source_type", "has_cross_site_ancestor",
	}
}

// isUniqueConstraintError reports whether the SQLite error is a unique-
// index violation. Source cookies sometimes have duplicate (host, name,
// path, scheme, port) tuples after the source-side blocklist filter; we
// silently skip later duplicates rather than failing the whole batch.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed")
}
