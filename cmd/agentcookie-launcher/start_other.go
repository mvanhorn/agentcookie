//go:build !darwin

package main

import "syscall"

// start replaces the launcher with path. Only macOS uses the launcher; this
// keeps the command building and testable elsewhere.
func start(path string, argv, env []string) (int, error) {
	return 0, syscall.Exec(path, argv, env)
}
