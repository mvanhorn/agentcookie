package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

// fakeVerifier returns scripted verify results without a live cmux.
type fakeVerifier struct {
	results []sinkpush.VerifyResult
	called  bool
}

func (f *fakeVerifier) Verify(specs []sinkpush.VerifySpec) []sinkpush.VerifyResult {
	f.called = true
	return f.results
}

func TestReportCmuxVerify_Output(t *testing.T) {
	cases := []struct {
		name    string
		result  sinkpush.VerifyResult
		want    string
		notWant string
	}{
		{
			name:    "authenticated",
			result:  sinkpush.VerifyResult{Host: "github.com", State: sinkpush.AuthYes},
			want:    "github.com authenticated",
			notWant: "NOT authenticated",
		},
		{
			name:   "not authenticated carries guidance",
			result: sinkpush.VerifyResult{Host: "github.com", State: sinkpush.AuthNo, Detail: "log in to github.com once inside the cmux browser"},
			want:   "NOT authenticated -- log in to github.com once inside the cmux browser",
		},
		{
			name:   "unknown is non-scary",
			result: sinkpush.VerifyResult{Host: "github.com", State: sinkpush.AuthUnknown, Detail: "eval error: boom"},
			want:   "github.com unknown (eval error: boom)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &fakeVerifier{results: []sinkpush.VerifyResult{tc.result}}
			var buf bytes.Buffer
			// nil filter -> builtin specs are non-empty, so Verify is invoked.
			reportCmuxVerify(v, nil, &buf)
			if !v.called {
				t.Fatal("Verify should have been called")
			}
			got := buf.String()
			if !strings.Contains(got, tc.want) {
				t.Errorf("output %q missing %q", got, tc.want)
			}
			if tc.notWant != "" && strings.Contains(got, tc.notWant) {
				t.Errorf("output %q should not contain %q", got, tc.notWant)
			}
		})
	}
}

func TestReportCmuxVerify_NoSpecsNoProbe(t *testing.T) {
	// A domain filter that matches no bound-session host means no probe at all.
	v := &fakeVerifier{}
	var buf bytes.Buffer
	reportCmuxVerify(v, []string{"%example.com"}, &buf)
	if v.called {
		t.Error("Verify must not be called when no bound-session host is in scope")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

// runCmuxSync validates the mode flags before any Chrome/keychain/cmux I/O,
// mirroring `source`. These cases exercise that gate without touching the
// system.
func TestCmuxSyncFlagValidation(t *testing.T) {
	reset := func(once, watch bool) {
		cmuxSyncOnce = once
		cmuxSyncWatch = watch
	}
	t.Cleanup(func() { reset(false, false) })

	t.Run("neither --once nor --watch errors", func(t *testing.T) {
		reset(false, false)
		err := runCmuxSync(&cobra.Command{}, nil)
		if err == nil || !strings.Contains(err.Error(), "either --once") {
			t.Fatalf("expected mode-required error, got %v", err)
		}
	})

	t.Run("both --once and --watch errors", func(t *testing.T) {
		reset(true, true)
		err := runCmuxSync(&cobra.Command{}, nil)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected mutual-exclusion error, got %v", err)
		}
	})
}

// errKeychainAccess is a representative missing-grant failure matching what
// SafeStoragePasswordFor returns when access is denied: the operator-facing
// prose plus the ErrKeychainNoGrant sentinel that drives classification.
var errKeychainAccess = fmt.Errorf("read Chrome Safe Storage from Keychain (did you grant access?): exit status 1: %w", chrome.ErrKeychainNoGrant)

func stubCmuxSyncPassword(t *testing.T, pw string, err error) {
	t.Helper()
	orig := cmuxSyncPasswordFor
	cmuxSyncPasswordFor = func(chrome.Browser) (string, error) { return pw, err }
	t.Cleanup(func() { cmuxSyncPasswordFor = orig })
}

func stubCmuxExitFunc(t *testing.T) *int {
	t.Helper()
	code := -1
	orig := cmuxExitFunc
	cmuxExitFunc = func(c int) { code = c }
	t.Cleanup(func() { cmuxExitFunc = orig })
	return &code
}

func TestRunCmuxSync_ExitsZeroOnKeychainFailureInWatchMode(t *testing.T) {
	cmuxSyncOnce = false
	cmuxSyncWatch = true
	t.Cleanup(func() { cmuxSyncOnce = false; cmuxSyncWatch = false })

	stubCmuxSyncPassword(t, "", errKeychainAccess)
	exitCode := stubCmuxExitFunc(t)

	err := runCmuxSync(&cobra.Command{}, nil)
	if err != nil {
		t.Errorf("runCmuxSync should not return error on Keychain access failure in --watch, got: %v", err)
	}
	if *exitCode != 0 {
		t.Errorf("exitFunc called with %d, want 0", *exitCode)
	}
}

func TestRunCmuxSync_ReturnsErrorOnKeychainFailureInOnceMode(t *testing.T) {
	cmuxSyncOnce = true
	cmuxSyncWatch = false
	t.Cleanup(func() { cmuxSyncOnce = false; cmuxSyncWatch = false })

	stubCmuxSyncPassword(t, "", errKeychainAccess)
	exitCode := stubCmuxExitFunc(t)

	err := runCmuxSync(&cobra.Command{}, nil)
	if err == nil {
		t.Error("runCmuxSync should return error on Keychain failure in --once mode")
	}
	if *exitCode != -1 {
		t.Errorf("exitFunc must not be called in --once mode, got %d", *exitCode)
	}
}

func TestRunCmuxSync_ReturnsErrorOnNonKeychainFailureInWatchMode(t *testing.T) {
	cmuxSyncOnce = false
	cmuxSyncWatch = true
	t.Cleanup(func() { cmuxSyncOnce = false; cmuxSyncWatch = false })

	stubCmuxSyncPassword(t, "", errors.New("network timeout: connection refused"))
	exitCode := stubCmuxExitFunc(t)

	err := runCmuxSync(&cobra.Command{}, nil)
	if err == nil {
		t.Error("runCmuxSync should return error on non-Keychain failure even in --watch mode")
	}
	if *exitCode != -1 {
		t.Errorf("exitFunc must not be called for non-Keychain errors, got %d", *exitCode)
	}
}

func TestRunCmuxSync_ReturnsErrorOnLockedKeychainInWatchMode(t *testing.T) {
	// A locked keychain is transient: --watch must exit non-zero so launchd's
	// KeepAlive retries once it unlocks, NOT exit 0 (which would stop the sync
	// permanently). This is the PR #107 misclassification fix at the call site:
	// the error carries ErrKeychainLocked even though it reads as an access
	// failure, so IsKeychainAccessError is false and exitFunc is never called.
	cmuxSyncOnce = false
	cmuxSyncWatch = true
	t.Cleanup(func() { cmuxSyncOnce = false; cmuxSyncWatch = false })

	lockedErr := fmt.Errorf("read Keychain (did you grant access?): login keychain is locked: %w", chrome.ErrKeychainLocked)
	stubCmuxSyncPassword(t, "", lockedErr)
	exitCode := stubCmuxExitFunc(t)

	err := runCmuxSync(&cobra.Command{}, nil)
	if err == nil {
		t.Error("runCmuxSync should return error (non-zero) on a locked keychain in --watch mode")
	}
	if *exitCode != -1 {
		t.Errorf("exitFunc must not be called for a locked keychain, got %d", *exitCode)
	}
}

func TestDeltaCookies(t *testing.T) {
	a := chrome.Cookie{HostKey: ".x.com", Name: "a", Path: "/", Value: "1"}
	b := chrome.Cookie{HostKey: ".x.com", Name: "b", Path: "/", Value: "2", ExpiresUTC: 100}
	full := []chrome.Cookie{a, b}

	// Empty state: everything is new (the startup full push).
	if got := deltaCookies(full, map[string]uint64{}); len(got) != 2 {
		t.Fatalf("empty state should push all, got %d", len(got))
	}

	state := hashCookieSet(full)

	// Unchanged set: nothing to push.
	if got := deltaCookies(full, state); len(got) != 0 {
		t.Fatalf("unchanged set should be empty delta, got %d", len(got))
	}

	// Value change re-queues only that cookie.
	a2 := a
	a2.Value = "rotated"
	if got := deltaCookies([]chrome.Cookie{a2, b}, state); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("expected only the changed cookie, got %+v", got)
	}

	// Expiry change alone re-queues too.
	b2 := b
	b2.ExpiresUTC = 200
	if got := deltaCookies([]chrome.Cookie{a, b2}, state); len(got) != 1 || got[0].Name != "b" {
		t.Fatalf("expected only the expiry-changed cookie, got %+v", got)
	}

	// A new cookie under a different path is a distinct identity.
	c := chrome.Cookie{HostKey: ".x.com", Name: "a", Path: "/api", Value: "1"}
	if got := deltaCookies([]chrome.Cookie{a, b, c}, state); len(got) != 1 || got[0].Path != "/api" {
		t.Fatalf("expected only the new path-scoped cookie, got %+v", got)
	}
}

func TestHashCookieSet_PrunesDeleted(t *testing.T) {
	a := chrome.Cookie{HostKey: ".x.com", Name: "a", Path: "/", Value: "1"}
	b := chrome.Cookie{HostKey: ".x.com", Name: "b", Path: "/", Value: "2"}
	state := hashCookieSet([]chrome.Cookie{a, b})
	if len(state) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(state))
	}
	// Rebuilding from the current (smaller) set drops the deleted cookie.
	state = hashCookieSet([]chrome.Cookie{a})
	if len(state) != 1 {
		t.Fatalf("rebuild should prune deletions, got %d entries", len(state))
	}
}
