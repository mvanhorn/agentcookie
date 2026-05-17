// Package clicker auto-dismisses Chrome's per-connection "Allow remote
// debugging" dialog so the agentcookie sink can attach in attach mode
// without any user click at runtime. The dialog is a Chrome browser-chrome
// UI element (not a DOM element) and cannot be reached via CDP. macOS
// Accessibility API can reach it, which is why this package exists.
//
// Activation contract:
//
//   - The user grants agentcookie Accessibility permission once at install
//     time (System Settings > Privacy & Security > Accessibility).
//   - At runtime, the sink runs a short osascript poll alongside each attach
//     attempt; when Chrome shows the Allow dialog, osascript clicks Allow.
//
// This file holds the install-time accessibility-grant helpers. The runtime
// click loop lives in clicker.go.
package clicker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// settingsAccessibilityURL deep-links to the Accessibility pane in macOS
// System Settings. macOS 13+ uses this URL scheme; older versions are out
// of scope (agentcookie is macOS 14+ throughout).
const settingsAccessibilityURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"

// ErrAccessibilityTimeout is returned when EnsureGranted's polling loop
// runs out before the user grants Accessibility.
var ErrAccessibilityTimeout = errors.New("clicker: Accessibility permission was not granted within the deadline")

// ErrAccessibilityNotGranted is returned by IsGranted's err return when a
// concrete failure mode is preferred over a (false, nil) result. Most
// callers should just check the bool.
var ErrAccessibilityNotGranted = errors.New("clicker: Accessibility permission is not granted")

// IsGranted reports whether the calling process has macOS Accessibility
// permission. Probes System Events; returns true when the probe round-trips
// successfully and false when System Events refuses with the canonical
// "not authorized" error.
//
// This probe is itself a System Events action, which means the calling
// process needs Accessibility to RUN the probe. macOS shrugs around this
// chicken-and-egg by returning a specific error string the first time the
// process tries; we parse that error.
func IsGranted(ctx context.Context) (bool, error) {
	// Cheap probe: just ask System Events to enumerate processes. If
	// Accessibility is granted, this succeeds. If not, osascript returns
	// "Application isn't running" or "not allowed assistive access".
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application "System Events" to count processes`)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	s := strings.ToLower(string(out))
	// macOS error strings we expect on a denied state.
	deniedMarkers := []string{
		"not allowed",
		"assistive access",
		"-1743",       // common osascript error code for accessibility denial
		"-25211",      // alternative accessibility error
		"is not allowed assistive access",
	}
	for _, m := range deniedMarkers {
		if strings.Contains(s, m) {
			return false, nil
		}
	}
	// Some other osascript failure; surface it.
	return false, fmt.Errorf("clicker: probe System Events: %w (%s)", err, strings.TrimSpace(string(out)))
}

// OpenAccessibilitySettings opens the macOS System Settings Accessibility
// pane via open(1). Returns nil on success; the open call returns even
// before System Settings has finished loading.
func OpenAccessibilitySettings(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/open", settingsAccessibilityURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clicker: open Accessibility settings: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureGranted blocks until the calling process has Accessibility, opening
// System Settings on first need. Polls every pollInterval (default 1s) up
// to timeout. Apple does not allow programmatic granting; the user must
// flip the toggle in System Settings.
//
// On macOS 15 the toggle live-updates the trusted-process list without
// requiring agentcookie to relaunch, so polling is sufficient.
func EnsureGranted(ctx context.Context, timeout time.Duration, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = 1 * time.Second
	}

	granted, err := IsGranted(ctx)
	if err != nil {
		return err
	}
	if granted {
		return nil
	}

	// Not granted yet: open the relevant settings pane and start polling.
	if err := OpenAccessibilitySettings(ctx); err != nil {
		// Even if open fails, keep polling. The user might already have
		// the settings open from a previous run.
		_ = err
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		granted, err := IsGranted(ctx)
		if err != nil {
			return err
		}
		if granted {
			return nil
		}
	}
	return ErrAccessibilityTimeout
}
