package chromeconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// localStateFile is the per-installation Chrome preferences file at the
// user-data-dir root. The chrome://inspect/#remote-debugging toggle persists
// here under devtools.remote_debugging.user-enabled. Verified in
// docs/research/chrome-144-remote-debugging.md.
const localStateFile = "Local State"

// remoteDebuggingPrefPath is the Local State JSON key path for the toggle.
const (
	prefDevtools         = "devtools"
	prefRemoteDebugging  = "remote_debugging"
	prefUserEnabledKey   = "user-enabled"
)

// IsRemoteDebuggingPrefSet returns true when Chrome's Local State at
// userDataDir has devtools.remote_debugging.user-enabled set to true. Does
// not require Chrome to be running.
func IsRemoteDebuggingPrefSet(userDataDir string) (bool, error) {
	path := filepath.Join(userDataDir, localStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("chromeconn: read Local State: %w", err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("chromeconn: parse Local State: %w", err)
	}
	dev, _ := state[prefDevtools].(map[string]any)
	if dev == nil {
		return false, nil
	}
	rd, _ := dev[prefRemoteDebugging].(map[string]any)
	if rd == nil {
		return false, nil
	}
	v, ok := rd[prefUserEnabledKey].(bool)
	if !ok {
		return false, nil
	}
	return v, nil
}

// SetRemoteDebuggingPref writes devtools.remote_debugging.user-enabled=true
// into Chrome's Local State at userDataDir, preserving every other key. Uses
// an atomic temp-file-rename write so a crash mid-write does not corrupt
// Chrome's preferences.
//
// Chrome should not be running when this is called: a running Chrome
// overwrites Local State periodically and on exit, which clobbers the write.
func SetRemoteDebuggingPref(userDataDir string) error {
	path := filepath.Join(userDataDir, localStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("chromeconn: %s not found; launch Chrome at least once before enabling remote debugging", path)
		}
		return fmt.Errorf("chromeconn: read Local State: %w", err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("chromeconn: parse Local State: %w", err)
	}
	dev, _ := state[prefDevtools].(map[string]any)
	if dev == nil {
		dev = map[string]any{}
	}
	rd, _ := dev[prefRemoteDebugging].(map[string]any)
	if rd == nil {
		rd = map[string]any{}
	}
	rd[prefUserEnabledKey] = true
	dev[prefRemoteDebugging] = rd
	state[prefDevtools] = dev

	out, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("chromeconn: marshal Local State: %w", err)
	}
	tmp := path + ".agentcookie.tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("chromeconn: write temp Local State: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chromeconn: rename Local State: %w", err)
	}
	return nil
}

// IsChromeRunning reports whether any Google Chrome process is currently
// running. Cheap pgrep equivalent; does not assert which user-data-dir.
func IsChromeRunning() (bool, error) {
	out, err := exec.Command("/usr/bin/pgrep", "-x", "Google Chrome").CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("chromeconn: pgrep Chrome: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return len(out) > 0, nil
}

// QuitChromeGracefully tells Chrome to quit via osascript, giving Chrome a
// chance to save session state, persist preferences, and close cleanly.
// Waits up to timeout for the process to exit; falls back to a friendly
// error if Chrome refuses (some pinned tabs or modal dialogs can block
// quit).
func QuitChromeGracefully(ctx context.Context, timeout time.Duration) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application "Google Chrome" to quit`)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chromeconn: osascript quit Chrome: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := IsChromeRunning()
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("chromeconn: Chrome did not exit within %s; a modal dialog or pinned tab may be blocking quit", timeout)
}

// LaunchChrome relaunches Chrome.app via macOS open(1). Returns once open
// returns, which is typically before Chrome has finished initializing. Use
// WaitForRemoteDebugging to know when CDP is actually available.
func LaunchChrome(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-a", "Google Chrome")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chromeconn: open Google Chrome: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WaitForRemoteDebugging polls for DevToolsActivePort in userDataDir up to
// timeout. Returns the discovered Endpoint on success. Use after LaunchChrome
// to wait until Chrome has finished initializing remote debugging.
func WaitForRemoteDebugging(ctx context.Context, userDataDir string, timeout time.Duration) (Endpoint, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ep, err := Discover(userDataDir)
		if err == nil {
			return ep, nil
		}
		if !errors.Is(err, ErrRemoteDebuggingNotEnabled) {
			return Endpoint{}, err
		}
		select {
		case <-ctx.Done():
			return Endpoint{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return Endpoint{}, fmt.Errorf("chromeconn: DevToolsActivePort did not appear within %s", timeout)
}
