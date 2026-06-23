//go:build !darwin || !cgo

package chrome

import "errors"

func safeStoragePasswordViaKeybaseFor(_, _ string) (string, error) {
	return "", errors.New("safeStoragePasswordViaKeybase: requires darwin+cgo build")
}

// keychainDefaultLocked is unsupported off darwin+cgo; report "not locked" with
// an error so callers treat lock status as unknown and do not short-circuit.
func keychainDefaultLocked() (bool, error) {
	return false, errors.New("keychainDefaultLocked: requires darwin+cgo build")
}
