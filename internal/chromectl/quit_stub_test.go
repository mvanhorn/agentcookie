package chromectl

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeChrome stands in for osascript, pgrep and pkill. Chrome is up until
// a signal arrives; the AppleScript quit itself never stops it, which is
// what happens when automation is refused or unanswered.
type fakeChrome struct {
	mu       sync.Mutex
	running  bool
	osaCalls int
	signals  []string
}

func (f *fakeChrome) setRunning(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = v
}

func (f *fakeChrome) snapshot() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.osaCalls, append([]string(nil), f.signals...)
}

// stubChrome swaps every seam for f and clears the refusal memo before
// and after the test, so no test touches the real Chrome or leaks state.
func stubChrome(t *testing.T, osa func(ctx context.Context) ([]byte, error)) *fakeChrome {
	t.Helper()
	f := &fakeChrome{running: true}
	origOsa, origRunning, origSignal, origBound := runOsascript, isRunning, sendSignal, appleScriptTimeout
	appleEventsRefused.Store(false)
	t.Cleanup(func() {
		runOsascript, isRunning, sendSignal, appleScriptTimeout = origOsa, origRunning, origSignal, origBound
		appleEventsRefused.Store(false)
	})
	runOsascript = func(ctx context.Context, _ string) ([]byte, error) {
		f.mu.Lock()
		f.osaCalls++
		f.mu.Unlock()
		return osa(ctx)
	}
	isRunning = func() (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.running, nil
	}
	sendSignal = func(_ context.Context, sig string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.signals = append(f.signals, sig)
		f.running = false
		return nil
	}
	return f
}

func osaNotAuthorized(context.Context) ([]byte, error) {
	return []byte("execution error: Not authorized to send Apple events to Google Chrome. (-1743)"), errors.New("exit status 1")
}

func osaUserCanceled(context.Context) ([]byte, error) {
	return []byte("execution error: User canceled. (-128)"), errors.New("exit status 1")
}

// osaHangs models an Automation prompt nobody answers.
func osaHangs(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func osaSucceeds(context.Context) ([]byte, error) { return nil, nil }

// quit runs QuitAndWait under a parent context long enough that only the
// code under test can make it return early.
func quit(t *testing.T, timeout time.Duration) (error, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	err := QuitAndWait(ctx, timeout)
	return err, time.Since(start)
}

func TestQuitAndWait_NotAuthorizedFallsBackEarly(t *testing.T) {
	f := stubChrome(t, osaNotAuthorized)
	err, elapsed := quit(t, 5*time.Second)
	if err != nil {
		t.Fatalf("QuitAndWait: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; -1743 must skip the polite poll", elapsed)
	}
	if _, sigs := f.snapshot(); !reflect.DeepEqual(sigs, []string{"TERM"}) {
		t.Errorf("signals = %v, want [TERM]", sigs)
	}
}

func TestQuitAndWait_UnansweredAppleScriptIsBounded(t *testing.T) {
	f := stubChrome(t, osaHangs)
	appleScriptTimeout = 100 * time.Millisecond
	err, elapsed := quit(t, 5*time.Second)
	if err != nil {
		t.Fatalf("QuitAndWait: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; a hung osascript must be cut off at its own bound", elapsed)
	}
	if _, sigs := f.snapshot(); !reflect.DeepEqual(sigs, []string{"TERM"}) {
		t.Errorf("signals = %v, want [TERM] (SIGTERM before any SIGKILL)", sigs)
	}
}

func TestQuitAndWait_RefusalIsRememberedForTheProcess(t *testing.T) {
	cases := []struct {
		name string
		osa  func(context.Context) ([]byte, error)
	}{
		{"not authorized", osaNotAuthorized},
		{"timeout", osaHangs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := stubChrome(t, tc.osa)
			appleScriptTimeout = 100 * time.Millisecond
			if err, _ := quit(t, 5*time.Second); err != nil {
				t.Fatalf("first QuitAndWait: %v", err)
			}
			f.setRunning(true)
			err, elapsed := quit(t, 5*time.Second)
			if err != nil {
				t.Fatalf("second QuitAndWait: %v", err)
			}
			if elapsed > 2*time.Second {
				t.Errorf("second quit took %v", elapsed)
			}
			calls, sigs := f.snapshot()
			if calls != 1 {
				t.Errorf("osascript calls = %d, want 1 (second quit must go straight to signals)", calls)
			}
			if !reflect.DeepEqual(sigs, []string{"TERM", "TERM"}) {
				t.Errorf("signals = %v, want [TERM TERM]", sigs)
			}
		})
	}
}

// -128 keeps its early fallback and is not remembered: the next quit
// tries AppleScript again.
func TestQuitAndWait_UserCanceledUnchanged(t *testing.T) {
	f := stubChrome(t, osaUserCanceled)
	err, elapsed := quit(t, 5*time.Second)
	if err != nil {
		t.Fatalf("QuitAndWait: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; -128 must skip the polite poll", elapsed)
	}
	f.setRunning(true)
	if err, _ := quit(t, 5*time.Second); err != nil {
		t.Fatalf("second QuitAndWait: %v", err)
	}
	calls, sigs := f.snapshot()
	if calls != 2 {
		t.Errorf("osascript calls = %d, want 2 (-128 must not be remembered)", calls)
	}
	if !reflect.DeepEqual(sigs, []string{"TERM", "TERM"}) {
		t.Errorf("signals = %v, want [TERM TERM]", sigs)
	}
}

// When AppleScript reports success but Chrome stays up for the whole
// polite window, SIGTERM still gets its own window before SIGKILL.
func TestQuitAndWait_SigtermHasItsOwnDeadline(t *testing.T) {
	f := stubChrome(t, osaSucceeds)
	if err, _ := quit(t, 400*time.Millisecond); err != nil {
		t.Fatalf("QuitAndWait: %v", err)
	}
	if _, sigs := f.snapshot(); !reflect.DeepEqual(sigs, []string{"TERM"}) {
		t.Errorf("signals = %v, want [TERM]", sigs)
	}
}
