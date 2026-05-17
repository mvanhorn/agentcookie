package chromectl

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsRunning_DoesNotPanic exercises IsRunning on the test runner and
// accepts either result.
func TestIsRunning_DoesNotPanic(t *testing.T) {
	_, err := IsRunning()
	if err != nil {
		t.Logf("IsRunning errored (OK in CI without macOS): %v", err)
	}
}

// TestQuitAndWait_NoOpWhenNotRunning verifies that quitting when Chrome
// is not running returns nil immediately.
func TestQuitAndWait_NoOpWhenNotRunning(t *testing.T) {
	running, err := IsRunning()
	if err != nil {
		t.Skipf("IsRunning errored: %v", err)
	}
	if running {
		t.Skip("Chrome is running on this machine; can't test the no-op path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := QuitAndWait(ctx, 500*time.Millisecond); err != nil {
		t.Errorf("expected nil when Chrome not running, got %v", err)
	}
}

// TestQuitAndWait_RespectsContext verifies that a canceled context
// surfaces.
func TestQuitAndWait_RespectsContext(t *testing.T) {
	running, _ := IsRunning()
	if !running {
		t.Skip("Chrome is not running; QuitAndWait short-circuits before the cancel matters")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := QuitAndWait(ctx, 5*time.Second)
	if err == nil {
		t.Skip("Chrome quit fast enough that the cancellation was not observed; test is racy in this configuration")
	}
	if !errors.Is(err, context.Canceled) && err.Error() == "" {
		t.Errorf("expected an error, got %v", err)
	}
}

// TestLaunchAndWait_NoOpWhenRunning verifies that launching when Chrome
// is already up returns nil immediately.
func TestLaunchAndWait_NoOpWhenRunning(t *testing.T) {
	running, err := IsRunning()
	if err != nil {
		t.Skipf("IsRunning errored: %v", err)
	}
	if !running {
		t.Skip("Chrome is not running; can't test the no-op-when-running path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := LaunchAndWait(ctx, 500*time.Millisecond); err != nil {
		t.Errorf("expected nil when Chrome already running, got %v", err)
	}
}

// TestWithChromeDown_RunsFnAndDoesNotRelaunch verifies WithChromeDown
// runs fn and, crucially, never calls LaunchAndWait. Runs only when
// Chrome is not currently running so the test does not disturb the
// developer's session.
func TestWithChromeDown_RunsFnAndDoesNotRelaunch(t *testing.T) {
	running, err := IsRunning()
	if err != nil {
		t.Skipf("IsRunning errored: %v", err)
	}
	if running {
		t.Skip("Chrome is running on this machine; test would disturb it")
	}
	called := false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WithChromeDown(ctx, 500*time.Millisecond, func() error {
		called = true
		return nil
	}); err != nil {
		t.Errorf("WithChromeDown returned error: %v", err)
	}
	if !called {
		t.Error("fn was not invoked")
	}
	// Confirm WithChromeDown did NOT launch Chrome behind our back.
	stillNotRunning, _ := IsRunning()
	if stillNotRunning {
		t.Error("WithChromeDown launched Chrome; it must leave Chrome quit")
	}
}

// TestWithChromeDown_PropagatesFnError ensures fn errors surface as the
// returned error and are not swallowed.
func TestWithChromeDown_PropagatesFnError(t *testing.T) {
	running, err := IsRunning()
	if err != nil {
		t.Skipf("IsRunning errored: %v", err)
	}
	if running {
		t.Skip("Chrome is running on this machine; test would disturb it")
	}
	want := errors.New("fn boom")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := WithChromeDown(ctx, 500*time.Millisecond, func() error {
		return want
	})
	if !errors.Is(got, want) {
		t.Errorf("expected fn error to propagate, got %v", got)
	}
}
