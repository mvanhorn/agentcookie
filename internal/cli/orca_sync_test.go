package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runOrcaSync validates the mode flags before any Chrome/keychain/orca I/O,
// mirroring cmux-sync. These cases exercise that gate without touching the
// system.
func TestOrcaSyncFlagValidation(t *testing.T) {
	reset := func(once, watch bool) {
		orcaSyncOnce = once
		orcaSyncWatch = watch
	}
	t.Cleanup(func() { reset(false, false) })

	t.Run("neither --once nor --watch errors", func(t *testing.T) {
		reset(false, false)
		err := runOrcaSync(&cobra.Command{}, nil)
		if err == nil || !strings.Contains(err.Error(), "either --once") {
			t.Fatalf("expected mode-required error, got %v", err)
		}
	})

	t.Run("both --once and --watch errors", func(t *testing.T) {
		reset(true, true)
		err := runOrcaSync(&cobra.Command{}, nil)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected mutual-exclusion error, got %v", err)
		}
	})
}

func TestOrcaSyncSummary(t *testing.T) {
	t.Run("no pane open", func(t *testing.T) {
		s := orcaSyncSummary(0, 0, 0)
		if !strings.Contains(s, "no orca browser pane is open") {
			t.Fatalf("expected open-a-pane note, got %q", s)
		}
	})

	t.Run("injected", func(t *testing.T) {
		s := orcaSyncSummary(2, 17, 0)
		if !strings.Contains(s, "injected 17 cookies into 2 orca pane(s)") {
			t.Fatalf("expected injected summary, got %q", s)
		}
	})

	t.Run("dbsc note appended", func(t *testing.T) {
		s := orcaSyncSummary(1, 10, 3)
		if !strings.Contains(s, "skipped 3 device-bound") {
			t.Fatalf("expected DBSC note, got %q", s)
		}
	})
}
