package macapp

import "golang.org/x/sys/unix"

// swap exchanges two directories atomically (renamex_np RENAME_SWAP).
func swap(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }
