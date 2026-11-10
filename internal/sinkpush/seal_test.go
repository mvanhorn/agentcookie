package sinkpush

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// stubMissingMasterKey keeps adapter fixtures independent of the host Keychain.
// Stub both operations: a temporary HOME or PATH cannot isolate /usr/bin/security.
func stubMissingMasterKey(t *testing.T) {
	t.Helper()
	stubMasterKey(t, func() bool { return false }, func() ([]byte, error) {
		t.Error("master key should not be read when it is absent")
		return nil, keystore.ErrMasterKeyMissing
	})
}

func stubMasterKey(t *testing.T, exists func() bool, read func() ([]byte, error)) {
	t.Helper()
	originalExists, originalRead := masterKeyExists, readMasterKey
	masterKeyExists, readMasterKey = exists, read
	t.Cleanup(func() { masterKeyExists, readMasterKey = originalExists, originalRead })
}

func TestMaybeSeal_MissingMasterKey(t *testing.T) {
	stubMissingMasterKey(t)
	got, err := maybeSeal("fixture-value")
	if err != nil || got != "fixture-value" {
		t.Fatalf("maybeSeal = %q, %v; want unchanged plaintext", got, err)
	}
}

func TestMaybeSeal_MasterKeyReadError(t *testing.T) {
	want := errors.New("fixture key unavailable")
	stubMasterKey(t, func() bool { return true }, func() ([]byte, error) { return nil, want })
	got, err := maybeSeal("fixture-value")
	if got != "" || !errors.Is(err, want) {
		t.Fatalf("maybeSeal = %q, %v; want wrapped key error and no plaintext", got, err)
	}
}

func TestMaybeSeal_FixtureMasterKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, keystore.MasterKeyBytes)
	stubMasterKey(t, func() bool { return true }, func() ([]byte, error) { return key, nil })
	got, err := maybeSeal("fixture-value")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, SealedPrefix) {
		t.Fatalf("missing sealed envelope prefix: %q", got)
	}
	envelope, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, SealedPrefix))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := keystore.Unseal(key, envelope)
	if err != nil || string(plain) != "fixture-value" {
		t.Fatalf("unseal = %q, %v; want fixture plaintext", plain, err)
	}
}
