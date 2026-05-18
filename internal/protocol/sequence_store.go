package protocol

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SequenceStore is the persistence backend a SequenceTracker reads at
// startup and writes to on every accepted envelope. The default
// implementation is fileSequenceStore at ~/.agentcookie/sequence.json
// (mode 0600). Tests inject MemorySequenceStore or a path under
// t.TempDir() to keep the on-disk file out of the user's home.
type SequenceStore interface {
	// Load returns the persisted map of source-hostname to highest
	// accepted sequence. A missing file is not an error; Load returns
	// an empty map and nil. A present-but-corrupt file IS an error,
	// surfaced so the sink can refuse to start.
	Load() (map[string]int64, error)
	// Save writes state atomically (write-tmp-then-rename) at mode 0600.
	Save(state map[string]int64) error
}

// fileSequenceStore writes JSON to a path on disk. Atomic via
// CreateTemp + Rename, mirroring internal/state/state.go.Writer.Save.
type fileSequenceStore struct {
	path string
}

// NewFileSequenceStore returns a SequenceStore backed by path. The
// parent directory is created (mode 0700) on the first Save when
// missing. path is typically filepath.Join(home, ".agentcookie",
// "sequence.json").
func NewFileSequenceStore(path string) SequenceStore {
	return &fileSequenceStore{path: path}
}

// DefaultSequencePath is the canonical on-disk location of the
// persistent replay-defense state.
func DefaultSequencePath(home string) string {
	return filepath.Join(home, ".agentcookie", "sequence.json")
}

func (s *fileSequenceStore) Load() (map[string]int64, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int64{}, nil
		}
		return nil, fmt.Errorf("read sequence state %s: %w", s.path, err)
	}
	// Empty file is treated as fresh state (no high-water marks yet).
	if len(data) == 0 {
		return map[string]int64{}, nil
	}
	state := map[string]int64{}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse sequence state %s: %w (delete this file to reset replay-defense state)", s.path, err)
	}
	return state, nil
}

func (s *fileSequenceStore) Save(state map[string]int64) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ensure sequence dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-sequence-*.json")
	if err != nil {
		return fmt.Errorf("create tmp sequence file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort chmod on the tmp file before write; final rename
	// preserves these bits on the destination.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod tmp sequence file: %w", err)
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("encode sequence state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close tmp sequence file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename sequence file into place: %w", err)
	}
	return nil
}

// MemorySequenceStore is an in-memory SequenceStore for tests. Captures
// every Save so test assertions can verify write-through behavior.
type MemorySequenceStore struct {
	State     map[string]int64
	SaveCount int
	// FailLoad, when non-nil, is returned from Load. Lets tests
	// exercise the sink-refuse-to-start path without writing a
	// corrupt file to disk.
	FailLoad error
	// FailSave, when non-nil, is returned from every Save call. Lets
	// tests exercise the write-through failure path.
	FailSave error
}

// NewMemorySequenceStore returns a MemorySequenceStore seeded with the
// given state. Pass nil for an empty store.
func NewMemorySequenceStore(seed map[string]int64) *MemorySequenceStore {
	st := map[string]int64{}
	for k, v := range seed {
		st[k] = v
	}
	return &MemorySequenceStore{State: st}
}

func (m *MemorySequenceStore) Load() (map[string]int64, error) {
	if m.FailLoad != nil {
		return nil, m.FailLoad
	}
	out := map[string]int64{}
	for k, v := range m.State {
		out[k] = v
	}
	return out, nil
}

func (m *MemorySequenceStore) Save(state map[string]int64) error {
	if m.FailSave != nil {
		return m.FailSave
	}
	m.SaveCount++
	cp := map[string]int64{}
	for k, v := range state {
		cp[k] = v
	}
	m.State = cp
	return nil
}
