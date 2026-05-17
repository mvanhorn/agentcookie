package chrome

import (
	"bytes"
	"crypto/sha256"
)

// stripAppBoundPrefix detects and removes the per-host_key prefix Chrome 127+
// embeds in cookie plaintext on macOS. Observed empirically against
// Chrome 146.0.7680.80 on 2026-05-16: every cookie's decrypted plaintext for a
// given host_key starts with an identical 32-byte sequence, and the sequence
// matches SHA-256(host_key). Cookies for different host_keys carry different
// prefixes. Pre-127 Chrome plaintext has no prefix and is returned unchanged.
//
// Decision: strip only when the first 32 bytes match SHA-256(host_key) exactly.
// Any other shape (pre-127 plaintext, future Chrome changes, malformed data)
// passes through. This is defensive against silently corrupting cookie values
// when Chrome changes its derivation in a future release.
func stripAppBoundPrefix(plaintext []byte, hostKey string) []byte {
	if len(plaintext) < sha256.Size {
		return plaintext
	}
	want := sha256.Sum256([]byte(hostKey))
	if !bytes.Equal(plaintext[:sha256.Size], want[:]) {
		return plaintext
	}
	return plaintext[sha256.Size:]
}

// prependAppBoundPrefix is the inverse of stripAppBoundPrefix: prepends the
// SHA256(host_key) prefix before v10 encryption so Chrome 127+ accepts the
// cookie on decrypt. Without the prefix, Chrome silently drops the cookie
// on the next launch (cookie passes SQLite write but doesn't survive the
// in-memory load).
func prependAppBoundPrefix(plaintext []byte, hostKey string) []byte {
	prefix := sha256.Sum256([]byte(hostKey))
	out := make([]byte, sha256.Size+len(plaintext))
	copy(out, prefix[:])
	copy(out[sha256.Size:], plaintext)
	return out
}
