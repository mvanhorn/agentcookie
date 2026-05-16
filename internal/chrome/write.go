package chrome

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// chromeEpochDeltaMicros is the number of microseconds between the Unix epoch
// (1970-01-01) and Chrome's WebKit epoch (1601-01-01). Used for the *_utc
// columns in the Cookies table.
const chromeEpochDeltaMicros int64 = 11644473600 * 1000 * 1000

// WriteCookies upserts each cookie into the destination Chrome SQLite Cookies
// database, re-encrypting Value with the destination's AES key. Chrome must NOT
// be running on the destination machine during this call (Chrome holds an
// exclusive lock on Cookies when up). The spike documents this limitation; live
// injection via CDP lands in U4.
func WriteCookies(dbPath string, cookies []Cookie, key []byte) (int, error) {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal=WAL&_busy_timeout=2000")
	if err != nil {
		return 0, fmt.Errorf("open destination cookies db: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return 0, fmt.Errorf("ping destination cookies db (is Chrome running?): %w", err)
	}

	// Discover the actual schema so we can target whatever Chrome version is on
	// this machine without hardcoding every Chromium release's column list.
	cols, err := readTableColumns(db, "cookies")
	if err != nil {
		return 0, fmt.Errorf("read cookies schema: %w", err)
	}

	insertSQL, args := buildUpsert(cols)

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare upsert (%s): %w", insertSQL, err)
	}
	defer stmt.Close()

	nowChrome := time.Now().UnixMicro() + chromeEpochDeltaMicros

	written := 0
	for _, c := range cookies {
		encrypted, err := encryptValue(c.Value, key)
		if err != nil {
			return written, fmt.Errorf("encrypt %s/%s: %w", c.HostKey, c.Name, err)
		}
		row := args(rowInput{
			cookie:        c,
			encrypted:     encrypted,
			creationUTC:   nowChrome,
			lastUpdateUTC: nowChrome,
		})
		if _, err := stmt.Exec(row...); err != nil {
			return written, fmt.Errorf("upsert %s/%s: %w", c.HostKey, c.Name, err)
		}
		written++
	}

	if err := tx.Commit(); err != nil {
		return written, fmt.Errorf("commit tx: %w", err)
	}
	return written, nil
}

func encryptValue(plaintext string, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	mode := cipher.NewCBCEncrypter(block, iv)
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+pad)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	ct := make([]byte, len(padded))
	mode.CryptBlocks(ct, padded)
	out := append([]byte("v10"), ct...)
	return out, nil
}

func readTableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid          int
			name, ctype  string
			notNull, pk  int
			dfltValue    sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

type rowInput struct {
	cookie        Cookie
	encrypted     []byte
	creationUTC   int64
	lastUpdateUTC int64
}

// buildUpsert returns an INSERT ... ON CONFLICT statement targeting only the
// columns present in the live schema, plus a function that returns the matching
// args slice for a given Cookie.
func buildUpsert(cols map[string]bool) (string, func(rowInput) []any) {
	// Column order is fixed for stable arg-binding regardless of map iteration.
	type field struct {
		name string
		get  func(rowInput) any
	}
	candidates := []field{
		{"creation_utc", func(r rowInput) any { return r.creationUTC }},
		{"host_key", func(r rowInput) any { return r.cookie.HostKey }},
		{"top_frame_site_key", func(r rowInput) any { return "" }},
		{"name", func(r rowInput) any { return r.cookie.Name }},
		{"value", func(r rowInput) any { return "" }},
		{"encrypted_value", func(r rowInput) any { return r.encrypted }},
		{"path", func(r rowInput) any { return r.cookie.Path }},
		{"expires_utc", func(r rowInput) any { return r.cookie.ExpiresUTC }},
		{"is_secure", func(r rowInput) any { return r.cookie.IsSecure }},
		{"is_httponly", func(r rowInput) any { return r.cookie.IsHTTPOnly }},
		{"last_access_utc", func(r rowInput) any { return r.cookie.LastAccessUTC }},
		{"has_expires", func(r rowInput) any { return r.cookie.HasExpires }},
		{"is_persistent", func(r rowInput) any { return r.cookie.IsPersistent }},
		{"priority", func(r rowInput) any { return r.cookie.Priority }},
		{"samesite", func(r rowInput) any { return r.cookie.SameSite }},
		{"source_scheme", func(r rowInput) any { return r.cookie.SourceScheme }},
		{"source_port", func(r rowInput) any { return r.cookie.SourcePort }},
		{"last_update_utc", func(r rowInput) any { return r.lastUpdateUTC }},
		{"source_type", func(r rowInput) any { return 0 }},
		{"has_cross_site_ancestor", func(r rowInput) any { return 1 }},
	}

	var present []field
	for _, c := range candidates {
		if cols[c.name] {
			present = append(present, c)
		}
	}

	var names, placeholders, updates []string
	for _, f := range present {
		names = append(names, f.name)
		placeholders = append(placeholders, "?")
		// Skip primary-key columns and creation_utc from the UPDATE clause.
		switch f.name {
		case "host_key", "top_frame_site_key", "name", "path", "source_scheme", "source_port", "creation_utc":
			continue
		}
		updates = append(updates, fmt.Sprintf("%s=excluded.%s", f.name, f.name))
	}

	// Build conflict target from the columns that are actually present and form
	// the unique key on every modern Chromium schema.
	conflictCandidates := []string{"host_key", "top_frame_site_key", "name", "path", "source_scheme", "source_port"}
	var conflictCols []string
	for _, c := range conflictCandidates {
		if cols[c] {
			conflictCols = append(conflictCols, c)
		}
	}

	sql := fmt.Sprintf(
		"INSERT INTO cookies (%s) VALUES (%s) ON CONFLICT(%s) DO UPDATE SET %s",
		strings.Join(names, ","),
		strings.Join(placeholders, ","),
		strings.Join(conflictCols, ","),
		strings.Join(updates, ","),
	)

	build := func(r rowInput) []any {
		out := make([]any, len(present))
		for i, f := range present {
			out[i] = f.get(r)
		}
		return out
	}

	return sql, build
}
