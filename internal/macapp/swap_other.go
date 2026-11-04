//go:build !darwin

package macapp

import "errors"

func swap(a, b string) error { return errors.New("atomic swap is only supported on macOS") }
