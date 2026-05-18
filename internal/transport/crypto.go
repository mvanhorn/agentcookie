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
// The derivation always runs the secret through SHA-256 to produce
// the 32-byte AES key, regardless of input length. A v0.12 plan unit
// (U10) initially short-circuited 32-byte inputs as pass-through,
// reasoning that the pairing-derived HKDF output is already a
// uniformly random 32 bytes. That change was reverted because v0.11
// sinks and v0.12 sources cannot interoperate without a coordinated
// upgrade: the pass-through and the SHA-256 path produce different
// AES keys, and the AEAD tag fails on any mixed pair.
//
// Running SHA-256 over an already-uniform 32-byte input is
// structurally harmless (output is still uniformly random 32 bytes,
// distinguishable only by a trivial amount of compute). The entropy
// floor on legacy security.shared_secret at config load (also from
// U10) stays in place independently — that part is config-time
// validation and does not touch the wire.
func newGCM(secret string) (cipher.AEAD, error) {
	keyHash := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
