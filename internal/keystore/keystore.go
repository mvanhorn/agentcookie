// Package keystore manages per-peer symmetric keys derived during pairing.
//
// v0.1 stores each key as a file at ~/.config/agentcookie/keys/<peer>.json
// with mode 0600. macOS Keychain integration is a planned v0.2 follow-up; the
// API in this package is shaped so the storage backend can be swapped without
// changing callers.
package keystore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PeerKey is one paired peer's long-term symmetric key plus metadata.
type PeerKey struct {
	Peer         string    `json:"peer"`
	Key          []byte    `json:"key"`
	PairedAt     time.Time `json:"paired_at"`
	Fingerprint  string    `json:"fingerprint"`
	ProtocolVer  int       `json:"protocol_version"`
}

// Dir returns the keys subdirectory under the given config dir.
func Dir(configDir string) string {
	return filepath.Join(configDir, "keys")
}

// Path returns the on-disk path for a given peer's key file. peer is
// sanitized so it cannot escape the keys directory.
func Path(configDir, peer string) (string, error) {
	clean := sanitize(peer)
	if clean == "" {
		return "", errors.New("peer name cannot be empty")
	}
	return filepath.Join(Dir(configDir), clean+".json"), nil
}

// Save writes the key to disk with mode 0600. Creates the keys dir if needed.
func Save(configDir string, k *PeerKey) error {
	if err := os.MkdirAll(Dir(configDir), 0o700); err != nil {
		return fmt.Errorf("mkdir keys: %w", err)
	}
	path, err := Path(configDir, k.Peer)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	// Atomic write: write to tempfile + rename, mode 0600.
	tmp, err := os.CreateTemp(Dir(configDir), ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

// Load returns the key for the given peer.
func Load(configDir, peer string) (*PeerKey, error) {
	path, err := Path(configDir, peer)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no paired key for peer %q (run `agentcookie pair`)", peer)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var k PeerKey
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if len(k.Key) == 0 {
		return nil, fmt.Errorf("%s has empty key", path)
	}
	return &k, nil
}

// Delete removes the peer key. Idempotent: returns nil if already absent.
func Delete(configDir, peer string) error {
	path, err := Path(configDir, peer)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// List returns all paired peer names.
func List(configDir string) ([]string, error) {
	entries, err := os.ReadDir(Dir(configDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read keys dir: %w", err)
	}
	var peers []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		peers = append(peers, strings.TrimSuffix(name, ".json"))
	}
	return peers, nil
}

// sanitize replaces filesystem-hostile characters in a peer name so the
// computed path stays inside the keys directory.
func sanitize(peer string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			return r
		default:
			return '_'
		}
	}, peer)
	// Reject leading dots so we don't create dotfiles or escape.
	clean = strings.TrimLeft(clean, ".")
	return clean
}
