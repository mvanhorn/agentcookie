//go:build !darwin || !cgo

package chrome

import "errors"

func safeStoragePasswordViaKeybaseFor(_, _ string) (string, error) {
	return "", errors.New("safeStoragePasswordViaKeybase: requires darwin+cgo build")
}
