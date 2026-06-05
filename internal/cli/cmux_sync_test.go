package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestCmuxSyncSummary(t *testing.T) {
	// No DBSC skips: single injected line, no DBSC note.
	s := cmuxSyncSummary(12, 0)
	if !strings.Contains(s, "injected 12 cookies") {
		t.Errorf("missing injected count: %q", s)
	}
	if strings.Contains(s, "DBSC") {
		t.Errorf("should not mention DBSC when none skipped: %q", s)
	}
	// With DBSC skips: the explanatory note appears.
	s = cmuxSyncSummary(12, 3)
	if !strings.Contains(s, "skipped 3 device-bound") || !strings.Contains(s, "logged-out") {
		t.Errorf("missing DBSC note: %q", s)
	}
}
