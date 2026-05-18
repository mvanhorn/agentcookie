package sinkpush

import (
	"encoding/base64"
	"fmt"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// SealedPrefix mirrors pkg/sidecar.SealedPrefix. Sinkpush adapter
// writers use the same on-disk envelope shape as the cookie sidecar:
// "agc1:" + base64(seal(masterKey, plaintext)). PP CLIs that consume
// adapter session files (config.toml access_token, cookies.json,
// session.json fields) detect the prefix and unseal transparently.
const SealedPrefix = "agc1:"

// maybeSeal returns a sealed envelope when the agentcookie master key
// is available in the user's macOS Keychain; otherwise returns plain
// untouched. Adapters call this on every secret-bearing field they
// write to disk so a v0.12 sink with a Keychain installed produces
// sealed files automatically and a partial install (no master key
// yet) falls back to v0.11 plaintext shape.
//
// Errors only when the master key item exists but cannot be read or
// the seal step itself fails. Returns plain + nil otherwise.
func maybeSeal(plain string) (string, error) {
	if !keystore.MasterKeyExists() {
		return plain, nil
	}
	mk, err := keystore.ReadMasterKey()
	if err != nil {
		return "", fmt.Errorf("read master key: %w", err)
	}
	env, err := keystore.Seal(mk, []byte(plain))
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(env), nil
}
