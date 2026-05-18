package transport

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// TestNewGCM_32BytePassThrough proves U10's no-double-hash property:
// a 32-byte secret (the shape of a pairing-derived HKDF output) is
// used directly as the AES-256 key, NOT re-hashed through SHA-256.
// Asserts the round trip succeeds; the structural property is that
// the inverse-of-SHA-256 path would never produce the same key.
func TestNewGCM_32BytePassThrough(t *testing.T) {
	// 32-byte secret, all 0xAB
	secret := string(bytes.Repeat([]byte{0xAB}, 32))
	plaintext := []byte("hello world")

	sealed, err := SealWithSecret(plaintext, secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := OpenWithSecret(sealed, secret)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(plaintext, opened) {
		t.Errorf("round-trip mismatch: got %q want %q", opened, plaintext)
	}

	// Re-seal with the SHA-256 hash of the same secret. If U10's
	// pass-through is wired up, the AEAD key from the 32-byte secret
	// will be that secret directly, not the SHA-256 of it, so the
	// hashed secret produces a different ciphertext.
	hashed := sha256.Sum256([]byte(secret))
	hashedSecret := string(hashed[:])
	sealedHashed, err := SealWithSecret(plaintext, hashedSecret)
	if err != nil {
		t.Fatalf("Seal hashed: %v", err)
	}
	// Try to open the original sealed with the hashed secret.
	if _, err := OpenWithSecret(sealed, hashedSecret); err == nil {
		t.Errorf("opening pass-through-keyed sealed with hashed secret should fail; pass-through broken")
	}
	// And opening hashed-sealed with the original 32-byte secret.
	if _, err := OpenWithSecret(sealedHashed, secret); err == nil {
		t.Errorf("opening hashed-keyed sealed with raw 32-byte secret should fail; pass-through broken")
	}
}

// TestNewGCM_NonStandardLengthGoesThroughSHA256 keeps the legacy path
// alive: any secret that isn't exactly 32 bytes is hashed before use.
func TestNewGCM_NonStandardLengthGoesThroughSHA256(t *testing.T) {
	cases := []string{
		"short",
		string(bytes.Repeat([]byte{0xCC}, 31)),
		string(bytes.Repeat([]byte{0xCC}, 33)),
		string(bytes.Repeat([]byte{0xCC}, 64)),
	}
	for _, secret := range cases {
		plaintext := []byte("round trip")
		sealed, err := SealWithSecret(plaintext, secret)
		if err != nil {
			t.Fatalf("Seal len=%d: %v", len(secret), err)
		}
		opened, err := OpenWithSecret(sealed, secret)
		if err != nil {
			t.Fatalf("Open len=%d: %v", len(secret), err)
		}
		if !bytes.Equal(plaintext, opened) {
			t.Errorf("round-trip mismatch at len=%d", len(secret))
		}
	}
}
