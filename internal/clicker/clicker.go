package clicker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrDialogTimeout is returned by WaitForAndClick when the polling loop
// runs out before the Allow dialog appears. Common after a sink reconnect
// to a Chrome session that has already granted this client.
var ErrDialogTimeout = errors.New("clicker: Allow dialog did not appear within the deadline")

// ErrChromeNotRunning is returned when the AppleScript probe reports Chrome
// is not running. Distinct from ErrDialogTimeout so the sink can decide
// whether to retry attach or surface a Chrome-crashed error.
var ErrChromeNotRunning = errors.New("clicker: Google Chrome is not running")

// ErrAccessibilityDenied is returned when osascript fails because the
// calling process lacks Accessibility permission. The caller should route
// to EnsureGranted before retrying.
var ErrAccessibilityDenied = errors.New("clicker: process lacks Accessibility permission")

// WaitForAndClick polls osascript every pollInterval (default 250ms) up to
// timeout, watching for Chrome's Allow dialog and clicking it the moment
// it appears. Returns nil on the first successful click. Returns
// ErrDialogTimeout when the deadline passes without seeing a dialog (which
// is the expected state when Chrome has already granted this client).
//
// Designed to be fired concurrently with the chromedp attach call from the
// sink: the attach call blocks on the WebSocket handshake; if Chrome shows
// a dialog, WaitForAndClick dismisses it and the handshake completes.
func WaitForAndClick(ctx context.Context, timeout time.Duration, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		result, err := runClickScript(ctx)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(result, "OK:"):
			return nil
		case result == "NO_CHROME":
			return ErrChromeNotRunning
		case result == "NO_DIALOG":
			// Keep polling.
		default:
			// Unexpected output; log and treat as no-dialog to keep polling.
		}
		if time.Now().After(deadline) {
			return ErrDialogTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// IsDialogVisible reports whether the Allow dialog is currently up without
// clicking it. Useful for tests and dry-run diagnostics.
func IsDialogVisible(ctx context.Context) (bool, error) {
	result, err := runScript(ctx, allowDialogVisibleScript)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(result, "VISIBLE:") {
		return true, nil
	}
	return false, nil
}

// runClickScript executes the click AppleScript and returns its stdout
// (trimmed). Maps osascript Accessibility failures to ErrAccessibilityDenied.
func runClickScript(ctx context.Context) (string, error) {
	return runScript(ctx, allowDialogClickScript)
}

// runScript executes an osascript expression and returns the trimmed
// stdout. Distinguishes Accessibility denial from other errors via stderr.
func runScript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.ToLower(string(exitErr.Stderr))
			if isAccessibilityDenialString(stderr) {
				return "", ErrAccessibilityDenied
			}
			return "", fmt.Errorf("clicker: osascript: %w (%s)", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("clicker: osascript: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isAccessibilityDenialString returns true when stderr indicates the
// calling process lacks Accessibility. macOS surfaces this through several
// distinct error codes depending on the API entry point hit.
func isAccessibilityDenialString(s string) bool {
	markers := []string{
		"-1743",                          // user has not authorized this application to send AppleEvents
		"-25211",                         // accessibility API not enabled
		"not allowed assistive access",   // canonical post-Mojave error
		"requires access to be enabled",  // macOS 14+ variant
		"access for assistive devices",   // older macOS phrasing
	}
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
