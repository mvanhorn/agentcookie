// Package sidecar exposes the public reader API for agentcookie's
// cookie sidecar SQLite. PP CLIs and other tools import this package
// instead of internal/chrome so the cookie format can evolve without
// every downstream consumer pinning to an internal API.
//
// The sidecar lives at the path returned by SidecarPath (defaults to
// ~/.agentcookie/cookies-plain.db). The schema is Chrome-shaped so
// kooky-compatible libraries can read it directly. v0.12 introduced
// the option of storing the cookie `value` column as a sealed
// envelope; this package's ReadSidecar transparently auto-detects
// either format.
package sidecar

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// SealedPrefix identifies a sealed value-column entry. The bytes after
// the prefix are base64-encoded keystore.Seal output (12-byte nonce ||
// AES-256-GCM ciphertext || tag).
const SealedPrefix = "agc1:"

// Cookie is the public shape returned by ReadSidecar. Fields mirror
// the columns kooky-style consumers already inspect.
type Cookie struct {
	HostKey     string
	Name        string
	Value       string
	Path        string
	ExpiresUTC  int64
	IsSecure    bool
	IsHTTPOnly  bool
}

// DefaultPath returns the default sidecar location. agentcookie sink
// and downstream PP CLIs both call this so there is one definition.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentcookie", "cookies-plain.db"), nil
}

// ReadSidecar opens the sidecar SQLite at path and returns every
// cookie row. Sealed `value` entries (prefixed with SealedPrefix) are
// transparently unsealed using the master key in the macOS Keychain.
// Plaintext entries pass through unchanged.
//
// Returns the underlying error from keystore.ReadMasterKey when sealed
// entries are present but the master key is unavailable -- callers
// should treat that as fatal rather than returning an empty cookie
// list to downstream consumers.
func ReadSidecar(path string) ([]Cookie, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("sidecar: open %s: %w", path, err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT host_key, name, value, path, expires_utc, is_secure, is_httponly FROM cookies`)
	if err != nil {
		return nil, fmt.Errorf("sidecar: query %s: %w", path, err)
	}
	defer rows.Close()

	var (
		out       []Cookie
		masterKey []byte
		keyErr    error
	)
	for rows.Next() {
		var c Cookie
		var isSecure, isHTTPOnly int
		if err := rows.Scan(&c.HostKey, &c.Name, &c.Value, &c.Path, &c.ExpiresUTC, &isSecure, &isHTTPOnly); err != nil {
			return nil, fmt.Errorf("sidecar: scan row: %w", err)
		}
		c.IsSecure = isSecure != 0
		c.IsHTTPOnly = isHTTPOnly != 0

		if strings.HasPrefix(c.Value, SealedPrefix) {
			// Lazy-load the master key on first sealed row so a
			// fully-plaintext sidecar never needs Keychain access.
			if masterKey == nil && keyErr == nil {
				masterKey, keyErr = keystore.ReadMasterKey()
			}
			if keyErr != nil {
				return nil, fmt.Errorf("sidecar: read master key (required for sealed entries): %w", keyErr)
			}
			plain, err := unsealValue(masterKey, c.Value)
			if err != nil {
				return nil, fmt.Errorf("sidecar: unseal %s/%s: %w", c.HostKey, c.Name, err)
			}
			c.Value = plain
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sidecar: iterate rows: %w", err)
	}
	return out, nil
}

// SealValue returns the on-disk form of a value-column entry sealed
// under masterKey. Used by the sink writer; exported here so tests
// and tooling can construct sealed sidecar files without taking a
// dependency on internal/chrome.
func SealValue(masterKey []byte, plaintext string) (string, error) {
	env, err := keystore.Seal(masterKey, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(env), nil
}

// unsealValue is the inverse of SealValue.
func unsealValue(masterKey []byte, sealed string) (string, error) {
	if !strings.HasPrefix(sealed, SealedPrefix) {
		return "", errors.New("sidecar: not a sealed value")
	}
	raw, err := base64.StdEncoding.DecodeString(sealed[len(SealedPrefix):])
	if err != nil {
		return "", fmt.Errorf("sidecar: base64 decode: %w", err)
	}
	plain, err := keystore.Unseal(masterKey, raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
