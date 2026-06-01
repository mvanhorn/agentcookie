package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/secretsbus"
)

var (
	revokeForce bool
)

var secretRevokeCmd = &cobra.Command{
	Use:   "revoke <name>",
	Short: "Undo a project's adoption (remove its manifest or bus directory)",
	Long: `agentcookie secret revoke removes a project from the agentcookie sync.

Behavior depends on how the project was adopted:

  - explicit-manifest: deletes the agentcookie.toml that adopted it.
    Default is to prompt before deleting unless --force is passed.

  - pp-cli-derived: emits an instruction telling the user how to silence
    the PP CLI auto-detect (drop an agentcookie.toml with sync.default = false
    in the well-known manifest dir). The PP CLI itself is not touched.

  - legacy-v1: removes the bus directory at ~/.agentcookie/secrets/<name>/.
    Equivalent to ` + "`agentcookie secret rm <name>`" + ` (this command wraps it).

After revoke, the next source push will not include the revoked project's
secrets. The sink still has whatever was last delivered; remove it on the
sink separately if needed.`,
	Args: cobra.ExactArgs(1),
	RunE: runSecretRevoke,
}

func init() {
	secretRevokeCmd.Flags().BoolVarP(&revokeForce, "force", "f", false, "skip confirmation when deleting a manifest file")
	secretCmd.AddCommand(secretRevokeCmd)
}

func runSecretRevoke(cmd *cobra.Command, args []string) error {
	name := args[0]
	if !validBusName(name) {
		return fmt.Errorf("invalid project name %q", name)
	}
	home, _ := os.UserHomeDir()
	reg, _ := secretsbus.Discover(secretsbus.DiscoveryConfig{HomeDir: home})

	rp, ok := reg.Projects[name]
	if !ok {
		return fmt.Errorf("no such adopted project %q (run `agentcookie discover` to list known projects)", name)
	}

	switch rp.Kind {
	case secretsbus.SourceKindExplicitManifest:
		if !revokeForce {
			fmt.Fprintf(cmd.ErrOrStderr(), "this will delete %s. Continue? (re-run with --force to confirm)\n", rp.SourcePath)
			return fmt.Errorf("revoke aborted; use --force")
		}
		if err := os.Remove(rp.SourcePath); err != nil {
			return fmt.Errorf("delete manifest %s: %w", rp.SourcePath, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "revoked %s (deleted %s)\n", name, rp.SourcePath)
		return nil

	case secretsbus.SourceKindPPCLIDerived:
		fmt.Fprintf(cmd.OutOrStdout(), `%s is auto-detected from %s.
To silence the auto-detect, drop a manifest with sync.default = false:

  cat > %s/.agentcookie/manifests/%s.toml <<EOF
  schema_version = 2
  name = "%s"
  display_name = "%s (silenced)"
  [secrets.file]
  path = "%s"
  [sync]
  default = false
  EOF

Then re-run `+"`agentcookie discover`"+` to confirm.
`, name, rp.SourcePath, home, name, name, name, rp.ReadInPlacePath)
		return nil

	case secretsbus.SourceKindLegacyV1:
		// Wrap the existing `secret rm` behavior.
		busDir := rp.SourcePath
		if err := os.RemoveAll(busDir); err != nil {
			return fmt.Errorf("remove %s: %w", busDir, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "revoked %s (removed %s)\n", name, busDir)
		return nil

	default:
		return fmt.Errorf("unsupported tier %q for revoke", rp.Kind)
	}
}
