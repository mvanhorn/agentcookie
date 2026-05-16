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
			// The raw 32-byte key is passed through SealWithSecret's SHA256
			// derivation. Both sides do the same so the AES key matches.
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
