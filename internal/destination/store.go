package destination

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mvanhorn/agentcookie/internal/keystore"
)

func (e *Engine) encode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(e.key) > 0 {
		b, err = keystore.Seal(e.key, b)
		if err == nil {
			b = []byte(`{"sealed":"` + base64.StdEncoding.EncodeToString(b) + `"}`)
		}
	}
	return b, err
}
func (e *Engine) decode(b []byte, v any) error {
	var seal struct {
		Sealed string `json:"sealed"`
	}
	if json.Unmarshal(b, &seal) == nil && seal.Sealed != "" {
		if len(e.key) == 0 {
			return fmt.Errorf("master key required for sealed destination state")
		}
		raw, err := base64.StdEncoding.DecodeString(seal.Sealed)
		if err != nil {
			return err
		}
		b, err = keystore.Unseal(e.key, raw)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(b, v)
}
func (e *Engine) read(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return e.decode(b, v)
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (e *Engine) write(path string, v any) error {
	b, err := e.encode(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// appendLogs bounds buffering independently of group size while retaining a
// distinct recoverable record for every item. All bytes and the directory are
// synced before the caller is allowed to mutate browser state.
func (e *Engine) appendLogs(path string, entries []intent) error {
	if len(entries) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	buffer := bufio.NewWriterSize(f, 32<<10)
	for _, in := range entries {
		b, err := e.encode(in)
		if err != nil {
			return err
		}
		if _, err = buffer.Write(b); err != nil {
			return err
		}
		if err = buffer.WriteByte('\n'); err != nil {
			return err
		}
	}
	if err = buffer.Flush(); err != nil {
		return err
	}
	if err = e.fault("before-intent-log-sync"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return e.fault("after-intent-log-sync")
}
func (e *Engine) readLog(path string) ([]intent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []intent
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 256<<20)
	for scan.Scan() {
		var in intent
		if err = e.decode(scan.Bytes(), &in); err != nil {
			return out, err
		}
		out = append(out, in)
	}
	if err := scan.Err(); err != nil {
		return out, err
	}
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err = f.ReadAt(last[:], info.Size()-1); err != nil {
			return out, err
		}
		if last[0] != '\n' {
			return out, fmt.Errorf("unterminated intent journal tail")
		}
	}
	return out, nil
}

func (e *Engine) replaceLog(path string, entries []intent) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".intents-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	for _, in := range entries {
		b, encodeErr := e.encode(in)
		if encodeErr != nil {
			f.Close()
			return encodeErr
		}
		if _, err = f.Write(append(b, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}
