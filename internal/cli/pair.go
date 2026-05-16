package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Pair source and sink machines (handshake lands in U5)",
	Long: `Eventually 'agentcookie pair --as source' will print a one-time pairing
code that the sink machine consumes via 'agentcookie pair --as sink --code <code>
--peer <hostname>', deriving a per-peer symmetric key that's stored in the
OS keychain.

Until U5 of the AgentCookie roadmap ships, both machines share a hardcoded
secret carried in source.yaml / sink.yaml. Set the same value on both ends
manually; rotate before any non-private deployment.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("pair is not yet implemented (planned for U5); for now share the same security.shared_secret in source.yaml and sink.yaml on both machines")
	},
}
