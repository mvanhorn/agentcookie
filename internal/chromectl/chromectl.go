// Package chromectl is the macOS Chrome lifecycle helper used by every
// agentcookie writer that needs Chrome briefly quit while it touches
// Chrome's on-disk state (cookies SQLite, Local Storage LevelDB, IndexedDB
// LevelDB). One package, one ceremony, used by every writer.
//
// The ceremony:
//   1. Caller asks for Chrome to be quit
//   2. osascript "tell application Google Chrome to quit" sends an
//      AppleEvent that lets Chrome save session state and persist
//      preferences before exiting
//   3. Caller polls until the process is gone
//   4. Caller does its file writes
//   5. Caller asks Chrome to relaunch via open(1)
//   6. Caller polls until Chrome's user-data-dir is in a stable state
//      (its Local State file is being written, indicating Chrome boot
//      finished initial setup)
//
// Both polling steps are deadline-bounded so a stuck Chrome process
// surfaces as a typed error rather than a hang.
package chromectl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrQuitTimeout is returned when Chrome did not exit within the deadline.
// Usually means Chrome has a modal dialog (Save changes? Confirm leave?)
// blocking quit.
var ErrQuitTimeout = errors.New("chromectl: Chrome did not exit within the deadline; a modal dialog may be blocking quit")

// ErrLaunchTimeout is returned when Chrome failed to start up within the
// deadline. Rare; usually means /Applications/Google Chrome.app is
// missing or unsigned.
var ErrLaunchTimeout = errors.New("chromectl: Chrome did not become ready within the deadline")

// IsRunning reports whether any Google Chrome process is currently up.
// Uses pgrep against the process name (not the full command line).
func IsRunning() (bool, error) {
	out, err := exec.Command("/usr/bin/pgrep", "-x", "Google Chrome").CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("chromectl: pgrep Chrome: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// QuitAndWait sends Chrome a graceful AppleEvent quit and polls until the
// process is gone. Returns nil immediately if Chrome was not running.
// Use this before any write to Chrome's on-disk state.
func QuitAndWait(ctx context.Context, timeout time.Duration) error {
	running, err := IsRunning()
	if err != nil {
		return err
	}
	if !running {
		return nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application "Google Chrome" to quit`)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chromectl: osascript quit: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		running, err := IsRunning()
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
	}
	return ErrQuitTimeout
}

// LaunchAndWait launches Chrome via open(1) and polls until it is
// running. Returns nil immediately if Chrome is already up.
//
// "Running" here means the OS-level Chrome process exists. It does NOT
// imply Chrome has fully initialized its profile or finished writing
// startup data. Callers that need profile-ready Chrome should add a
// post-launch sleep or check for specific profile artifacts.
func LaunchAndWait(ctx context.Context, timeout time.Duration) error {
	running, err := IsRunning()
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-a", "Google Chrome")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chromectl: open Chrome: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		running, err := IsRunning()
		if err != nil {
			return err
		}
		if running {
			return nil
		}
	}
	return ErrLaunchTimeout
}

// WithChromeQuit is a convenience wrapper that quits Chrome, runs fn,
// then relaunches. Errors from fn are wrapped and returned; relaunch is
// attempted regardless of fn's success so Chrome is not left in the down
// state on a write failure.
//
// quitTimeout and launchTimeout cap the two boundary phases.
// fn runs with no time limit; the caller's context cancellation handles
// abort.
func WithChromeQuit(ctx context.Context, quitTimeout, launchTimeout time.Duration, fn func() error) error {
	if err := QuitAndWait(ctx, quitTimeout); err != nil {
		return err
	}
	fnErr := fn()
	launchErr := LaunchAndWait(ctx, launchTimeout)
	if fnErr != nil {
		return fnErr
	}
	if launchErr != nil {
		return launchErr
	}
	return nil
}
