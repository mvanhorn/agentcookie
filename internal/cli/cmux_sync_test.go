package cli

import (
	"bytes"
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
