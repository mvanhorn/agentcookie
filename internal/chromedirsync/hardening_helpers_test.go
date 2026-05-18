package chromedirsync

import "os"

// openRead is a tiny indirection that lets hardening_test.go open
// files without importing os directly in the same file.
func openRead(path string) (*os.File, error) { return os.Open(path) }
