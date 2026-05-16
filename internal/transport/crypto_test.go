package transport

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	secret := "test-secret-not-real"
	plaintext := []byte("hello agentcookie")
	sealed, err := SealWithSecret(plaintext, secret)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(sealed, plaintext) {
		t.Fatal("ciphertext equals plaintext (encryption did not run)")
	}
	got, err := OpenWithSecret(sealed, secret)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestSealOpenRejectsWrongSecret(t *testing.T) {
	sealed, err := SealWithSecret([]byte("payload"), "correct-secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenWithSecret(sealed, "wrong-secret"); err == nil {
		t.Fatal("expected error when opening with wrong secret, got nil")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	sealed, err := SealWithSecret([]byte("payload"), "secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	tampered := append([]byte{}, sealed...)
	// Flip the last byte; AES-GCM tag check catches it.
	tampered[len(tampered)-1] ^= 0x01
	if _, err := OpenWithSecret(tampered, "secret"); err == nil {
		t.Fatal("expected error when opening tampered ciphertext, got nil")
	}
}

func TestSealProducesDifferentCiphertextEachCall(t *testing.T) {
	a, err := SealWithSecret([]byte("payload"), "secret")
	if err != nil {
		t.Fatalf("seal a: %v", err)
	}
	b, err := SealWithSecret([]byte("payload"), "secret")
	if err != nil {
		t.Fatalf("seal b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("expected different ciphertexts on repeated seals (nonce should vary), got identical bytes")
	}
}
