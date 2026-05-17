package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// internalCmd is a hidden command group for implementation-detail helpers
// that the wizard and runbooks invoke but humans rarely call directly.
// Hidden from `agentcookie --help` to keep the user-facing surface clean.
var internalCmd = &cobra.Command{
	Use:    "internal",
	Short:  "Implementation-detail subcommands used by wizard and probes",
	Hidden: true,
}

var keychainProbeCmd = &cobra.Command{
	Use:   "keychain-probe",
	Short: "Test whether Chrome Safe Storage is readable via the kooky-CGO API path",
	Long: `keychain-probe calls Chrome Safe Storage via the same Apple Security
framework API that kooky's CGO build uses (github.com/keybase/go-keychain ->
SecItemCopyMatching). It is used by the agentcookie wizard's
set-keychain-access strategy loop to tell whether a given partition-list
or trust-list change actually makes the Keychain item readable to
kooky-using CLIs, not just to the legacy security CLI.

Prints "ok len=N" and exits 0 when the call succeeds (N is the password
byte length; the password itself is never printed). Prints "fail: <error>"
and exits 1 otherwise. A 5-second timeout detects the SecurityAgent-prompt
hang as a typed failure rather than a hang.

Run this from a LaunchAgent context (where the login keychain is auto-
unlocked) for accurate results. Run from SSH and it will almost certainly
fail with "interaction not allowed" regardless of the ACL/partition list,
because SSH sessions have the login keychain locked.`,
	RunE: runKeychainProbe,
}

var keychainProbeTimeoutSeconds int

func init() {
	keychainProbeCmd.Flags().IntVar(&keychainProbeTimeoutSeconds, "timeout-seconds", 5, "fail with ErrProbeTimedOut if the Keychain call does not return in this many seconds")
	internalCmd.AddCommand(keychainProbeCmd)
	rootCmd.AddCommand(internalCmd)
}

func runKeychainProbe(cmd *cobra.Command, args []string) error {
	timeout := time.Duration(keychainProbeTimeoutSeconds) * time.Second
	n, err := chrome.KeybaseKeychainProbe(timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fail: %v\n", err)
		return err
	}
	fmt.Fprintf(os.Stdout, "ok len=%d\n", n)
	return nil
}
