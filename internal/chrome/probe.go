package chrome

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// ProbeResult is the post-write sink-side health check against a freshly
// written Cookies SQLite. The check exercises the same v10 + AES-CBC path
// kooky v0.2.2 uses, so a green probe means the bridge is actually readable
// to PP CLIs on this machine, not just internally round-tripping.
type ProbeResult struct {
	Path string

	// RowsChecked is the number of cookies the probe successfully decrypted.
	// Zero is suspicious -- either the file is empty or all decrypts failed.
	RowsChecked int

	// AppBoundLeaks is the count of cookies whose decrypted plaintext starts
	// with SHA256(host_key). Non-zero means the App-Bound prefix is still
	// being emitted on write -- a U1 regression that would silently corrupt
	// every kooky v0.2.2 read.
	AppBoundLeaks int

	// MetaVersion is the value of meta.version in the SQLite. v0.9 expects
	// "18" (the U2 pin); anything else means the meta write regressed or
	// Chrome rewrote the file.
	MetaVersion string
}

// OK reports whether the bridge is healthy from a kooky-v0.2.2 reader's
// point of view: at least one row decrypted, no App-Bound prefix leaks,
// and meta.version pinned to "18".
func (r ProbeResult) OK() bool {
	return r.RowsChecked > 0 && r.AppBoundLeaks == 0 && r.MetaVersion == "18"
}

// Summary returns a one-line description of the probe result suitable for
// sink stderr.
func (r ProbeResult) Summary() string {
	if r.OK() {
		return fmt.Sprintf("probe ok: %d cookies round-tripped, meta.version=%s", r.RowsChecked, r.MetaVersion)
	}
	return fmt.Sprintf("probe FAIL: rows=%d app-bound-leaks=%d meta.version=%q", r.RowsChecked, r.AppBoundLeaks, r.MetaVersion)
}

// ProbeCookiesFile decrypts up to maxRows cookies from path using the same
// v10 + AES-CBC + macOS Keychain key path kooky v0.2.2 uses, then reads
// meta.version. Reports any App-Bound prefix leakage from the write path
// and the meta.version pin. Read-only; never modifies path.
//
// The bridge has multiple ways to silently break (Chrome rewrites cookies on
// launch, agentcookie write path regresses, the Keychain key changes between
// install and run). A failed probe in sink stderr exposes the break
// immediately instead of letting an agent run fail an hour later with no
// breadcrumb.
func ProbeCookiesFile(path string, key []byte, maxRows int) (ProbeResult, error) {
	result := ProbeResult{Path: path}
	if maxRows <= 0 {
		maxRows = 3
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return result, fmt.Errorf("probe open: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT host_key, encrypted_value FROM cookies WHERE LENGTH(encrypted_value) > 0 LIMIT ?`, maxRows)
	if err != nil {
		return result, fmt.Errorf("probe query cookies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var host string
		var enc []byte
		if err := rows.Scan(&host, &enc); err != nil {
			return result, fmt.Errorf("probe scan: %w", err)
		}
		plain, err := decryptValue(enc, key)
		if err != nil {
			return result, fmt.Errorf("probe decrypt %s: %w", host, err)
		}
		result.RowsChecked++
		hostHash := sha256.Sum256([]byte(host))
		if len(plain) >= sha256.Size && bytes.Equal([]byte(plain)[:sha256.Size], hostHash[:]) {
			result.AppBoundLeaks++
		}
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("probe iterate: %w", err)
	}

	// meta.version is best-effort; an empty result means the meta table or
	// row is missing, which surfaces in result.MetaVersion=="" rather than
	// as an error. The caller's OK() check fails in that case anyway.
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='version'`).Scan(&result.MetaVersion)

	return result, nil
}
