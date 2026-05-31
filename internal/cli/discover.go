package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/secretsbus"
)

var (
	discoverVerbose bool
)

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "List projects the secrets-bus discovery engine found, and what it skipped",
	Long: `agentcookie discover shows the secrets-bus v2 registry on this machine:

  - which projects have been adopted (via explicit agentcookie.toml,
    PP CLI auto-detect, or legacy v1 bus directory)
  - where each manifest came from
  - what file each project reads in place
  - how many keys each project would ship
  - skipped manifests with the reason (--verbose)

This is a debug/inspection command. It does not push, modify, or sync.

See docs/spec-agentcookie-secrets-bus-v2-adoption.md for the discovery
contract.`,
	RunE: runDiscover,
}

func init() {
	discoverCmd.Flags().BoolVarP(&discoverVerbose, "verbose", "v", false, "include skipped manifests and the reason for each skip")
}

type discoverJSONRow struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	SourcePath      string `json:"source_path"`
	ReadInPlacePath string `json:"read_in_place_path,omitempty"`
	DisplayName     string `json:"display_name,omitempty"`
	KeyCount        int    `json:"key_count"`
	SyncSummary     string `json:"sync_summary,omitempty"`
	Coverage        string `json:"coverage,omitempty"`
	CoverageDetail  string `json:"coverage_detail,omitempty"`
}

type discoverJSONOutput struct {
	Projects []discoverJSONRow `json:"projects"`
	Skipped  []discoverJSONRow `json:"skipped,omitempty"`
}

func runDiscover(cmd *cobra.Command, _ []string) error {
	home, _ := os.UserHomeDir()
	reg, errs := secretsbus.Discover(secretsbus.DiscoveryConfig{HomeDir: home})

	if common.JSON {
		out := discoverJSONOutput{}
		for _, name := range sortedRegistryKeys(reg) {
			out.Projects = append(out.Projects, projectToRow(reg.Projects[name]))
		}
		if discoverVerbose {
			for _, sk := range reg.Skipped {
				out.Skipped = append(out.Skipped, discoverJSONRow{
					Name:        sk.Name,
					Kind:        string(sk.Kind),
					SourcePath:  sk.SourcePath,
					SyncSummary: sk.SkippedReason,
				})
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}

	if len(reg.Projects) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no adopted projects found")
		if discoverVerbose && len(reg.Skipped) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			fmt.Fprintln(cmd.OutOrStdout(), "skipped:")
			for _, sk := range reg.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", sk.SourcePath, sk.SkippedReason)
			}
		}
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTIER\tREAD-IN-PLACE\tKEYS\tCOVERAGE")
	var mismatches []discoverJSONRow
	for _, name := range sortedRegistryKeys(reg) {
		rp := reg.Projects[name]
		row := projectToRow(rp)
		readPath := row.ReadInPlacePath
		if readPath == "" {
			readPath = "(legacy bus dir)"
		}
		coverage := row.Coverage
		if row.Coverage == "MISMATCH" {
			coverage = "MISMATCH (" + row.CoverageDetail + ")"
			mismatches = append(mismatches, row)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", row.Name, row.Kind, readPath, row.KeyCount, coverage)
	}
	_ = tw.Flush()

	if len(mismatches) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintf(cmd.OutOrStdout(), "%d CLI(s) have synced secrets that do not match the auth env var they read:\n", len(mismatches))
		for _, m := range mismatches {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", m.Name, m.CoverageDetail)
		}
	}

	if discoverVerbose {
		if len(reg.Skipped) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			fmt.Fprintln(cmd.OutOrStdout(), "skipped:")
			for _, sk := range reg.Skipped {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", sk.SourcePath, sk.SkippedReason)
			}
		}
		if len(errs) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			fmt.Fprintln(cmd.OutOrStdout(), "discovery errors:")
			for _, e := range errs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %v\n", e)
			}
		}
	}
	return nil
}

func projectToRow(rp *secretsbus.RegisteredProject) discoverJSONRow {
	row := discoverJSONRow{
		Name:            rp.Name,
		Kind:            string(rp.Kind),
		SourcePath:      rp.SourcePath,
		ReadInPlacePath: rp.ReadInPlacePath,
	}
	if rp.Manifest != nil {
		row.DisplayName = rp.Manifest.DisplayName
		row.KeyCount = len(rp.Manifest.Sync.Keys)
		if rp.Manifest.Sync.Default {
			row.SyncSummary = fmt.Sprintf("default=true, %d overrides", len(rp.Manifest.Sync.Keys))
		} else {
			row.SyncSummary = fmt.Sprintf("default=false, %d allowed", len(rp.Manifest.Sync.Keys))
		}
	}
	row.Coverage, row.CoverageDetail = secretCoverage(rp.Name, declaredKeysOf(rp))
	return row
}

func sortedRegistryKeys(reg *secretsbus.Registry) []string {
	keys := make([]string, 0, len(reg.Projects))
	for k := range reg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
