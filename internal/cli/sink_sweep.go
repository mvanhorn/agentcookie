package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

// liveCDPSweep closes stale about:blank tabs in the running Chrome at a
// CDP endpoint. Tests replace it so they never dial Chrome.
var liveCDPSweep = livecdp.SweepStaleBlankTabs

type sinkSweepTabsOptions struct {
	Endpoint string
	DryRun   bool
}

var sinkSweepTabsOpts sinkSweepTabsOptions

var sinkSweepTabsCmd = &cobra.Command{
	Use:   "sweep-tabs",
	Short: "Close the blank tabs older sink versions left in the live CDP Chrome",
	Long: `Earlier sink versions could leave an about:blank tab in the attached
Chrome on each live CDP inject, so a long-running sink piled up thousands
of them. Injects no longer open tabs. Run this once to close the ones already there.

It lists the browser's tabs, waits 60 seconds, lists them again, and closes
tabs that were about:blank both times. It never closes a page with a real
URL, a tab a client is attached to, a popup a page opened, a tab outside
Chrome's default browser context, or the last open page, and it always
leaves one stale blank tab.

It sweeps the live_cdp endpoint in sink.yaml. --endpoint sweeps another
Chrome instead. --dry-run lists the tabs it would close and closes none.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSinkSweepTabsWith(cmd.Context(), cmd.OutOrStdout(), common.ConfigDir, sinkSweepTabsOpts)
	},
}

func init() {
	sinkSweepTabsCmd.Flags().StringVar(&sinkSweepTabsOpts.Endpoint, "endpoint", "", "CDP endpoint to sweep instead of live_cdp.endpoint from sink.yaml")
	sinkSweepTabsCmd.Flags().BoolVar(&sinkSweepTabsOpts.DryRun, "dry-run", false, "list the tabs it would close and close none")
	sinkCmd.AddCommand(sinkSweepTabsCmd)
}

// runSinkSweepTabsWith runs one sweep pass and reports what it closed, or
// with DryRun what it would close.
func runSinkSweepTabsWith(ctx context.Context, out io.Writer, configDir string, opts sinkSweepTabsOptions) error {
	endpoint := opts.Endpoint
	if endpoint == "" {
		cfg, err := config.LoadSink(configDir)
		if err != nil {
			return fmt.Errorf("read sink.yaml (or pass --endpoint): %w", err)
		}
		if !cfg.LiveCDP.Enabled {
			return errors.New("live_cdp is not enabled in sink.yaml; pass --endpoint to sweep a Chrome by its CDP endpoint")
		}
		endpoint = cfg.LiveCDP.Endpoint
		if endpoint == "" {
			endpoint = livecdp.DefaultCDPEndpoint
		}
	}

	fmt.Fprintf(out, "Watching %s for %s for blank tabs that stay blank...\n", endpoint, livecdp.DefaultSweepMinAge)
	ids, err := liveCDPSweep(ctx, endpoint, livecdp.DefaultSweepMinAge, opts.DryRun)
	if opts.DryRun {
		fmt.Fprintf(out, "Would close %d stale about:blank tab(s) at %s.\n", len(ids), endpoint)
		for _, id := range ids {
			fmt.Fprintf(out, "  %s\n", id)
		}
	} else {
		fmt.Fprintf(out, "Closed %d stale about:blank tab(s) at %s.\n", len(ids), endpoint)
	}
	return err
}
