package cli

import (
	"fmt"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

// resolveTransportSecret returns the secret string passed into transport
// AES-GCM. Prefers a paired key (peer.hostname is set, keystore has an
// entry) over the legacy security.shared_secret YAML field. Returns an
// error if neither is available.
func resolveTransportSecret(configDir, peerHost, legacy string) (string, error) {
	if peerHost != "" {
		pk, err := keystore.Load(configDir, peerHost)
		if err == nil {
			// pk.Key is the 32-byte HKDF output from pairing. transport.newGCM
			// recognizes 32-byte secrets and uses them directly as the AES
			// key, skipping the SHA-256 step that legacy shared_secret
			// values go through (see internal/transport/crypto.go).
			return string(pk.Key), nil
		}
		if legacy == "" {
			return "", fmt.Errorf("no key for peer %q (run `agentcookie pair`): %w", peerHost, err)
		}
		// Paired key missing but legacy is set: use the legacy and warn callers
		// via the error chain. For now, just fall through silently.
	}
	if legacy == "" {
		return "", fmt.Errorf("no transport credential available: set peer.hostname (after pairing) or security.shared_secret (legacy)")
	}
	return legacy, nil
}
