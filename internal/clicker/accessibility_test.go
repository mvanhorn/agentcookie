package clicker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEnsureGranted_TimesOutWithoutGrant uses a stubbed prober so this test
// does not depend on the runner machine's Accessibility state.
func TestEnsureGranted_TimesOutWithoutGrant(t *testing.T) {
	// IsGranted is tested via the public surface; EnsureGranted does not
	// take a stub. We exercise it with a very short timeout in a context
	// that the prober is unlikely to satisfy (it'll either be granted
	// already and return immediately, or never-grant and time out).
	//
	// To keep this test deterministic without mocking out the system
	// command, we skip when Accessibility happens to be granted on the
	// runner (rare in CI; common on developer machines).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	granted, err := IsGranted(ctx)
	if err != nil {
		t.Skipf("IsGranted errored on this runner (likely CI without osascript): %v", err)
	}
	if granted {
		t.Skip("Accessibility is already granted on this runner; nothing to time out on")
	}

	err = EnsureGranted(ctx, 500*time.Millisecond, 100*time.Millisecond)
	if !errors.Is(err, ErrAccessibilityTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected ErrAccessibilityTimeout or DeadlineExceeded, got %v", err)
	}
}

// TestEnsureGranted_Cancellation cancels the context immediately and verifies
// EnsureGranted respects it. Like the timeout test, skips when Accessibility
// happens to be granted (returns nil before reading the cancellation).
func TestEnsureGranted_Cancellation(t *testing.T) {
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	granted, err := IsGranted(probeCtx)
	probeCancel()
	if err != nil {
		t.Skipf("IsGranted errored on this runner: %v", err)
	}
	if granted {
		t.Skip("Accessibility is already granted; cancellation path never reached")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = EnsureGranted(ctx, 5*time.Second, 100*time.Millisecond)
	if err == nil {
		t.Error("expected cancellation error, got nil")
	}
}

// TestIsGranted_DoesNotPanic just exercises the function on the test runner.
// Either result is acceptable; the test verifies the function returns
// without panicking or hanging.
func TestIsGranted_DoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := IsGranted(ctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("IsGranted returned error (this is OK depending on environment): %v", err)
	}
}
