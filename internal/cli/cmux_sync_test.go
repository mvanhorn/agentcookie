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
