package chrome

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// Cookie is the wire-friendly representation of one Chrome cookie. Value is
// plaintext after decryption; the wire format re-encrypts it with the sink's
// key on the other side.
type Cookie struct {
	HostKey       string `json:"host_key"`
	Name          string `json:"name"`
	Value         string `json:"value"`
	Path          string `json:"path"`
	ExpiresUTC    int64  `json:"expires_utc"`
	IsSecure      int    `json:"is_secure"`
	IsHTTPOnly    int    `json:"is_httponly"`
	LastAccessUTC int64  `json:"last_access_utc"`
	HasExpires    int    `json:"has_expires"`
	IsPersistent  int    `json:"is_persistent"`
	Priority      int    `json:"priority"`
	SameSite      int    `json:"samesite"`
	SourceScheme  int    `json:"source_scheme"`
	SourcePort    int    `json:"source_port"`
}

// DefaultCookiesPath returns the default Chrome cookies SQLite path on macOS.
func DefaultCookiesPath() string {
	b, _ := LookupBrowser(defaultBrowserName)
	return b.CookiesPath(defaultBrowserProfile)
}

// ReadCookiesForHost opens the given SQLite path read-only and returns all
// cookies whose host_key matches the LIKE pattern (use % wildcards for substring
// match, e.g. "%instacart.com").
func ReadCookiesForHost(dbPath, hostPattern string, key []byte) ([]Cookie, error) {
	// immutable=1 lets us read while Chrome holds its lock.
	dsn := fmt.Sprintf("file:%s?mode=ro&immutable=1", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cookies db: %w", err)
	}
	defer db.Close()

	query := `
SELECT host_key, name, encrypted_value, path,
       expires_utc, is_secure, is_httponly, last_access_utc,
       has_expires, is_persistent, priority, samesite,
       source_scheme, source_port
FROM cookies
WHERE host_key LIKE ?`
	rows, err := db.Query(query, hostPattern)
	if err != nil {
		return nil, fmt.Errorf("query cookies: %w", err)
	}
	defer rows.Close()

	var cookies []Cookie
	for rows.Next() {
		var (
			c              Cookie
			encryptedValue []byte
		)
		if err := rows.Scan(
			&c.HostKey, &c.Name, &encryptedValue, &c.Path,
			&c.ExpiresUTC, &c.IsSecure, &c.IsHTTPOnly, &c.LastAccessUTC,
			&c.HasExpires, &c.IsPersistent, &c.Priority, &c.SameSite,
			&c.SourceScheme, &c.SourcePort,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		plain, err := decryptValue(encryptedValue, key)
		if err != nil {
			return nil, fmt.Errorf("decrypt %s/%s: %w", c.HostKey, c.Name, err)
		}
		// Chrome 127+ on macOS embeds a 32-byte host-bound prefix in the
		// plaintext. Strip it so downstream consumers (CDP, dumps, fixtures)
		// see only the real cookie value.
		stripped := stripAppBoundPrefix([]byte(plain), c.HostKey)
		c.Value = string(stripped)
		cookies = append(cookies, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return cookies, nil
}

func decryptValue(encrypted, key []byte) (string, error) {
	if len(encrypted) < 3 {
		return "", fmt.Errorf("ciphertext too short (%d bytes)", len(encrypted))
	}
	prefix := string(encrypted[:3])
	if prefix != "v10" {
		return "", fmt.Errorf("unexpected ciphertext prefix %q (spike supports v10 only)", prefix)
	}
	ct := encrypted[3:]
	if len(ct)%aes.BlockSize != 0 {
		return "", fmt.Errorf("ciphertext length %d not a multiple of AES block size", len(ct))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	// Chrome on macOS uses 16 space chars as the IV, not 16 nulls.
	iv := bytes.Repeat([]byte{' '}, aes.BlockSize)
	mode := cipher.NewCBCDecrypter(block, iv)
	pt := make([]byte, len(ct))
	mode.CryptBlocks(pt, ct)
	// PKCS#7 unpad.
	if len(pt) == 0 {
		return "", fmt.Errorf("empty plaintext after decrypt")
	}
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(pt) {
		return "", fmt.Errorf("invalid PKCS#7 padding byte %d", pad)
	}
	return string(pt[:len(pt)-pad]), nil
}
