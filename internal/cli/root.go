// Package cli wires the cobra subcommand tree for the unified `agentcookie`
// binary.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Version is set at link time when releases ship; defaults to a dev tag here.
var Version = "0.0.1-dev"

// CommonFlags hold values resolved on every invocation.
type CommonFlags struct {
	ConfigDir string
	JSON      bool
}

var common CommonFlags

var rootCmd = &cobra.Command{
	Use:           "agentcookie",
	Short:         "Peer-to-peer Chrome session replication for AI agents",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `agentcookie keeps a "sink" machine's Chrome logged in by continuously
shipping cookies from a "source" machine's Chrome over an authenticated
peer-to-peer channel. The source is where you log in interactively; the
sink is where your AI agents act on your behalf.

Typical setup:

  agentcookie pair --as source        # on the laptop
  agentcookie pair --as sink ...      # on the Mac mini (uses the code above)
  agentcookie source --once           # one-shot sync on the laptop
  agentcookie sink                    # long-lived listener on the Mac mini

See examples/ in this repo for sample config files.`,
}

// Execute runs the root command. Called from main.
func Execute() {
	rootCmd.PersistentFlags().StringVar(&common.ConfigDir, "config-dir", defaultConfigDir(), "directory holding source.yaml, sink.yaml, allowlist.yaml")
	rootCmd.PersistentFlags().BoolVar(&common.JSON, "json", false, "emit machine-readable JSON output where the subcommand supports it")

	rootCmd.AddCommand(sourceCmd, sinkCmd, pairCmd, statusCmd, versionCmd)

	if err := rootCmd.Execute(); err != nil {
		// Cobra already prints usage on flag errors; surface RunE errors here.
		if !cobraReportedError(err) {
			fmt.Fprintln(os.Stderr, "agentcookie:", err)
		}
		os.Exit(1)
	}
}

func defaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentcookie"
	}
	return filepath.Join(home, ".config", "agentcookie")
}

// cobraReportedError filters cobra's flag-usage errors, which it already
// prints, from RunE errors that we need to surface ourselves.
func cobraReportedError(err error) bool {
	// Cobra wraps flag errors; for now we always print our own error and rely
	// on rootCmd's SilenceUsage=false to handle the rest. Reserved hook.
	return false
}
