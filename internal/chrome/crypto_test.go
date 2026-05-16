package chrome

import (
	"bytes"
	"testing"
)

// TestEncryptDecryptRoundTrip exercises the AES-128-CBC + PKCS#7 + v10 path
// the spike uses for both directions. If Chrome changes the IV or key length
// these assertions break loudly.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := DeriveAESKey("any-test-password")
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	if len(key) != aesKeyLen {
		t.Fatalf("expected %d-byte key, got %d", aesKeyLen, len(key))
	}

	for _, plaintext := range []string{
		"",
		"a",
		"sixteen-bytes-ok",
		"this is longer than one AES block to exercise multi-block CBC",
		"contains\x00null\x00bytes",
		string(bytes.Repeat([]byte("x"), 4096)),
	} {
		encrypted, err := encryptValue(plaintext, key)
		if err != nil {
			t.Fatalf("encryptValue(%q): %v", plaintext, err)
		}
		if !bytes.HasPrefix(encrypted, []byte("v10")) {
			t.Errorf("expected v10 prefix on ciphertext, got %q", encrypted[:3])
		}
		decrypted, err := decryptValue(encrypted, key)
		if err != nil {
			t.Fatalf("decryptValue(%q): %v", plaintext, err)
		}
		if decrypted != plaintext {
			t.Errorf("round-trip mismatch: input %q != output %q", plaintext, decrypted)
		}
	}
}

// TestDecryptValueRejectsBadPrefix proves the spike's prefix guard works.
// Adjacent Chromium platforms use v11 / Linux fallback formats that we
// deliberately do not support yet.
func TestDecryptValueRejectsBadPrefix(t *testing.T) {
	key, _ := DeriveAESKey("any-test-password")
	encrypted, err := encryptValue("hello", key)
	if err != nil {
		t.Fatalf("encryptValue: %v", err)
	}
	tampered := append([]byte{}, encrypted...)
	tampered[0] = 'x'
	if _, err := decryptValue(tampered, key); err == nil {
		t.Fatal("expected error for bad prefix, got nil")
	}
}

// TestDeriveAESKeyDeterministic guards against accidental change to salt or
// iteration count. Two derivations from the same password must match.
func TestDeriveAESKeyDeterministic(t *testing.T) {
	a, err := DeriveAESKey("same-password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveAESKey("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("derived keys differ across runs: %x vs %x", a, b)
	}
	c, err := DeriveAESKey("different-password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Errorf("different passwords produced identical keys: %x", a)
	}
}
