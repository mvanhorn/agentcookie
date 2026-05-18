// Package transport is the spike's shared-secret AES-GCM wrapper for the
// HTTP-over-tailnet sync channel. v0.1's real protocol replaces this with a
// pairing-handshake-derived key per peer.
package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// SealWithSecret encrypts plaintext under a key derived from the shared secret.
// Nonce is prepended to the ciphertext for transport.
func SealWithSecret(plaintext []byte, secret string) ([]byte, error) {
	gcm, err := newGCM(secret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// OpenWithSecret decrypts a payload produced by SealWithSecret. Returns an
// error on any auth/integrity failure.
func OpenWithSecret(ciphertext []byte, secret string) ([]byte, error) {
	gcm, err := newGCM(secret)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short (%d bytes)", len(ciphertext))
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt (wrong secret or tampered payload): %w", err)
	}
	return pt, nil
}

// newGCM returns an AES-256-GCM cipher derived from secret.
//
// Two paths:
//
//   - When secret is exactly 32 bytes long, it is treated as an
//     already-uniform key (the pairing-derived HKDF-SHA256 output is
//     32 bytes of uniformly random data) and used directly. No
//     redundant SHA-256 step.
//
//   - Otherwise, secret goes through SHA-256 to produce a 32-byte
//     AES key. This covers the legacy security.shared_secret YAML
//     path. The config layer rejects secrets below a 32-byte entropy
//     floor so attackers cannot drive this path with a weak secret.
func newGCM(secret string) (cipher.AEAD, error) {
	var key [32]byte
	if len(secret) == 32 {
		copy(key[:], secret)
	} else {
		key = sha256.Sum256([]byte(secret))
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
