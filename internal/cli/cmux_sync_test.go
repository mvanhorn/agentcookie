package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

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
