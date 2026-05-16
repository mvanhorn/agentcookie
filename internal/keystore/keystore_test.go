package keystore

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pk := &PeerKey{
		Peer:        "my-laptop.tail.ts.net",
		Key:         []byte("32-bytes-of-deterministic-key-aa"),
		PairedAt:    time.Now().UTC().Truncate(time.Second),
		Fingerprint: "deadbeef",
		ProtocolVer: 1,
	}
	if err := Save(dir, pk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir, "my-laptop.tail.ts.net")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Peer != pk.Peer {
		t.Errorf("peer mismatch: %q vs %q", got.Peer, pk.Peer)
	}
	if !bytes.Equal(got.Key, pk.Key) {
		t.Errorf("key bytes mismatch")
	}
	if got.Fingerprint != pk.Fingerprint {
		t.Errorf("fingerprint mismatch")
	}
	if got.ProtocolVer != pk.ProtocolVer {
		t.Errorf("protocol mismatch")
	}
}

func TestSaveCreatesFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes don't apply on Windows")
	}
	dir := t.TempDir()
	pk := &PeerKey{Peer: "test", Key: []byte("k"), PairedAt: time.Now()}
	if err := Save(dir, pk); err != nil {
		t.Fatal(err)
	}
	path, _ := Path(dir, "test")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected mode 0600, got %v", info.Mode().Perm())
	}
}

func TestSanitizationKeepsPathInsideDir(t *testing.T) {
	dir := t.TempDir()
	// A peer name containing path traversal must NOT escape the keys dir.
	pk := &PeerKey{Peer: "../escape", Key: []byte("k"), PairedAt: time.Now()}
	if err := Save(dir, pk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Walk the dir; ensure all files live under Dir(dir).
	root := Dir(dir)
	var found bool
	err := filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			found = true
			// Resolve absolute paths to compare prefixes safely.
			abs, _ := filepath.Abs(p)
			rootAbs, _ := filepath.Abs(root)
			if !pathHasPrefix(abs, rootAbs) {
				t.Errorf("key file escaped: %s not under %s", abs, rootAbs)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no key file written")
	}
}

func TestListReturnsSavedPeers(t *testing.T) {
	dir := t.TempDir()
	peers := []string{"a", "b", "c"}
	for _, p := range peers {
		if err := Save(dir, &PeerKey{Peer: p, Key: []byte("k"), PairedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(peers) {
		t.Errorf("expected %d peers, got %d (%v)", len(peers), len(got), got)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, &PeerKey{Peer: "x", Key: []byte("k"), PairedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "x"); err != nil {
		t.Fatalf("Delete first: %v", err)
	}
	if err := Delete(dir, "x"); err != nil {
		t.Errorf("Delete second (should be idempotent): %v", err)
	}
}

func TestLoadMissingPeer(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir, "no-such-peer"); err == nil {
		t.Fatal("expected error for missing peer, got nil")
	}
}

func pathHasPrefix(p, prefix string) bool {
	if p == prefix {
		return true
	}
	if len(p) <= len(prefix) {
		return false
	}
	return p[:len(prefix)+1] == prefix+string(os.PathSeparator)
}
