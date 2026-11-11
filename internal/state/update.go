package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// UpdateJSON serializes a durable read-modify-write across processes. The
// sidecar lock survives atomic replacement of the JSON inode. Corrupt state
// fails closed; it is never silently reset to zero.
func UpdateJSON(path string, update func(map[string]json.RawMessage) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	values := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("invalid durable source state: %w", err)
		}
		if values == nil {
			return fmt.Errorf("invalid null durable source state")
		}
	}
	if err := update(values); err != nil {
		return err
	}
	return NewWriter(path).saveAtomic(values)
}
