package keystore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// SealedEnvelope is the on-disk shape of a value sealed under the
// master key. The envelope is: 12-byte AES-GCM nonce || ciphertext ||
// 16-byte GCM tag. Total overhead per envelope is 28 bytes.
//
// The seal/unseal helpers handle the envelope shape; callers store and
// retrieve []byte directly.

const (
	nonceSize = 12
	gcmTagLen = 16
)

// Seal returns a sealed envelope around plaintext, using the given
// master key. The envelope is safe to store on disk under the user's
// regular file permissions because the master key has a Keychain ACL
// that restricts read access to allowlisted binaries.
//
// The master key must be exactly MasterKeyBytes (32) bytes.
func Seal(masterKey, plaintext []byte) ([]byte, error) {
	if len(masterKey) != MasterKeyBytes {
		return nil, fmt.Errorf("keystore.Seal: master key wrong length %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	envelope := make([]byte, 0, nonceSize+len(ct))
	envelope = append(envelope, nonce...)
	envelope = append(envelope, ct...)
	return envelope, nil
}

// Unseal returns the plaintext from an envelope created by Seal. Errors
// when the envelope is too short, the master key is wrong, or the GCM
// authentication tag does not match (the envelope was tampered with or
// corrupted).
func Unseal(masterKey, envelope []byte) ([]byte, error) {
	if len(masterKey) != MasterKeyBytes {
		return nil, fmt.Errorf("keystore.Unseal: master key wrong length %d", len(masterKey))
	}
	if len(envelope) < nonceSize+gcmTagLen {
		return nil, errors.New("keystore.Unseal: envelope too short")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := envelope[:nonceSize]
	ct := envelope[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("keystore.Unseal: envelope corrupt or master key mismatch: %w", err)
	}
	return pt, nil
}
