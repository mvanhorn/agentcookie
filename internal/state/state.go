// Package state is the shared state-file format that the source watcher and
// the sink daemon both write to ~/.agentcookie/. `agentcookie status` reads
// both files to surface live health.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SourceState is the source watcher's observable state, written on every push.
type SourceState struct {
	Role           string    `json:"role"`
	LastPush       time.Time `json:"last_push,omitempty"`
	LastPushCount  int       `json:"last_push_count"`
	TotalPushes    int       `json:"total_pushes"`
	TotalFailures  int       `json:"total_failures"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	SinkURL        string    `json:"sink_url"`
}

// SinkState is the sink daemon's observable state, written on every accepted
// /sync write.
type SinkState struct {
	Role           string    `json:"role"`
	LastWrite      time.Time `json:"last_write,omitempty"`
	LastWriteCount int       `json:"last_write_count"`
	LastWriteMode  string    `json:"last_write_mode"`
	TotalWrites    int       `json:"total_writes"`
	TotalDropped   int       `json:"total_dropped"`
	TotalRejects   int       `json:"total_rejects"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
	ListenAddr     string    `json:"listen_addr"`
	CDPManaged     bool      `json:"cdp_managed"`

	// LastAdapterResults records the v0.11 sinkpush adapter run that
	// followed LastWrite. One entry per registered adapter. Empty when
	// no sync has yet triggered a sinkpush run. `agentcookie status`
	// surfaces this; `agentcookie wizard verify-adapters` reads it.
	LastAdapterResults []AdapterResult `json:"last_adapter_results,omitempty"`
}

// AdapterResult is the per-adapter outcome of the most recent sinkpush
// run. Mirrors sinkpush.Result but lives in the state package to avoid
// state depending on sinkpush; sink.go converts between the two before
// writing.
type AdapterResult struct {
	Name          string    `json:"name"`
	Pushed        int       `json:"pushed,omitempty"`
	Invalid       int       `json:"invalid,omitempty"`
	Skipped       bool      `json:"skipped,omitempty"`
	SkippedReason string    `json:"skipped_reason,omitempty"`
	Err           string    `json:"error,omitempty"`
	RanAt         time.Time `json:"ran_at"`
}

// Paths under ~/.agentcookie/.
func SourcePath(home string) string { return filepath.Join(home, ".agentcookie", "source-state.json") }
func SinkPath(home string) string   { return filepath.Join(home, ".agentcookie", "sink-state.json") }

// Writer serializes JSON state file writes from multiple goroutines.
type Writer struct {
	path string
	mu   sync.Mutex
}

// NewWriter returns a Writer that targets path. The parent directory is
// created on first Save.
func NewWriter(path string) *Writer {
	return &Writer{path: path}
}

// Save writes v as JSON to the writer's path atomically (write-then-rename).
func (w *Writer) Save(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".tmp-state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, w.path)
}

// LoadSource reads source-state.json. Returns nil, nil when the file does not
// exist (the daemon may not have written yet).
func LoadSource(path string) (*SourceState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s SourceState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadSink reads sink-state.json. Returns nil, nil when missing.
func LoadSink(path string) (*SinkState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s SinkState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
