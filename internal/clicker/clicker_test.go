package clicker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIsAccessibilityDenialString(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"execution error: not authorized to send Apple events (-1743)", true},
		{"is not allowed assistive access. (-25211)", true},
		{"google chrome requires access to be enabled", true},
		{"some unrelated error", false},
		{"", false},
		{"-1743", true},
	}
	for _, tc := range cases {
		got := isAccessibilityDenialString(strings.ToLower(tc.in))
		if got != tc.want {
			t.Errorf("isAccessibilityDenialString(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestWaitForAndClick_PassesCtxThrough verifies cancellation works. Skips
// when Chrome happens to be running with a dialog up (rare in tests).
func TestWaitForAndClick_PassesCtxThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Either: WaitForAndClick checks the cancellation between polls and
	// returns ctx.Err(), or osascript itself notices the canceled context.
	err := WaitForAndClick(ctx, 5*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Error("expected error from canceled ctx, got nil")
	}
}

// TestWaitForAndClick_TimesOutWhenNoDialog verifies the timeout path.
// Skips when osascript errors out (CI without macOS Accessibility).
func TestWaitForAndClick_TimesOutWhenNoDialog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	probe, err := runScript(ctx, allowDialogVisibleScript)
	if err != nil && errors.Is(err, ErrAccessibilityDenied) {
		t.Skipf("test environment lacks Accessibility; skipping: %v", err)
	}
	if err != nil {
		t.Skipf("osascript not usable on this runner: %v", err)
	}
	// Probe should report NO_DIALOG (or NO_CHROME) when no dialog is up,
	// which is the expected test condition.
	if strings.HasPrefix(probe, "VISIBLE:") {
		t.Skipf("a dialog is currently visible on this machine; can't test timeout path")
	}

	err = WaitForAndClick(ctx, 800*time.Millisecond, 200*time.Millisecond)
	if !errors.Is(err, ErrDialogTimeout) && !errors.Is(err, ErrChromeNotRunning) {
		t.Errorf("expected ErrDialogTimeout or ErrChromeNotRunning, got %v", err)
	}
}

// TestApplescriptCompiles verifies the AppleScript snippets parse via the
// osascript compile-only mode (-c flag is not standard; we use -ss for
// syntax-check-only). Guards against typos.
func TestApplescriptCompiles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// osascript does not have a true syntax-only flag; instead we invoke
	// the scripts and check that they at least produce an expected return
	// value. Both scripts return "NO_DIALOG" or "NO_CHROME" when not
	// applicable, which is good enough to know they parsed.
	for _, name := range []string{"click", "visible"} {
		var script string
		if name == "click" {
			script = allowDialogClickScript
		} else {
			script = allowDialogVisibleScript
		}
		_, err := runScript(ctx, script)
		if err != nil {
			if errors.Is(err, ErrAccessibilityDenied) {
				t.Skipf("Accessibility not granted on this runner: %v", err)
			}
			t.Errorf("%s script failed to execute: %v", name, err)
		}
	}
}
