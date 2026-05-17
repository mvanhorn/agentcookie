//go:build !darwin || !cgo

package chrome

import "errors"

func safeStoragePasswordViaKeybase() (string, error) {
	return "", errors.New("safeStoragePasswordViaKeybase: requires darwin+cgo build")
}
