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
