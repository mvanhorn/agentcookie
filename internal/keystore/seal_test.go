package keystore

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestSealUnseal_RoundTrip(t *testing.T) {
	key := make([]byte, MasterKeyBytes)
	_, _ = rand.Read(key)

	cases := [][]byte{
		[]byte(""),
		[]byte("short"),
		bytes.Repeat([]byte{0xAB}, 1024),
		bytes.Repeat([]byte{0xCD}, 64*1024),
	}
	for _, pt := range cases {
		env, err := Seal(key, pt)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		got, err := Unseal(key, env)
		if err != nil {
			t.Fatalf("Unseal: %v", err)
		}
		if !bytes.Equal(got, pt) {
			t.Errorf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestUnseal_RejectsWrongKey(t *testing.T) {
	keyA := make([]byte, MasterKeyBytes)
	keyB := make([]byte, MasterKeyBytes)
	_, _ = rand.Read(keyA)
	_, _ = rand.Read(keyB)

	env, err := Seal(keyA, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Unseal(keyB, env); err == nil {
		t.Error("unsealing with wrong key should fail; got nil")
	}
}

func TestUnseal_RejectsCorruptedEnvelope(t *testing.T) {
	key := make([]byte, MasterKeyBytes)
	_, _ = rand.Read(key)

	env, err := Seal(key, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Flip one byte in the ciphertext region (past the nonce).
	env[nonceSize+1] ^= 0x40
	if _, err := Unseal(key, env); err == nil {
		t.Error("unsealing a tampered envelope should fail; got nil")
	}
}

func TestSeal_RejectsWrongLengthKey(t *testing.T) {
	cases := [][]byte{
		nil,
		make([]byte, 16),
		make([]byte, 31),
		make([]byte, 33),
		make([]byte, 64),
	}
	for _, k := range cases {
		if _, err := Seal(k, []byte("x")); err == nil {
			t.Errorf("Seal with key len=%d should fail", len(k))
		}
		// Even an obviously-junk envelope should also be rejected on bad
		// key length before any crypto runs.
		if _, err := Unseal(k, make([]byte, 40)); err == nil {
			t.Errorf("Unseal with key len=%d should fail", len(k))
		}
	}
}

func TestSeal_FreshNonceEachCall(t *testing.T) {
	key := make([]byte, MasterKeyBytes)
	_, _ = rand.Read(key)

	envA, _ := Seal(key, []byte("same plaintext"))
	envB, _ := Seal(key, []byte("same plaintext"))
	if bytes.Equal(envA[:nonceSize], envB[:nonceSize]) {
		t.Error("two seals with same plaintext produced identical nonce; randomness broken")
	}
	if bytes.Equal(envA, envB) {
		t.Error("two seals with same plaintext produced identical envelope; randomness broken")
	}
}
