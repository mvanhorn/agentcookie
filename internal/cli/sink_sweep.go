package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

// liveCDPSweep closes stale about:blank tabs in the running Chrome at a
// CDP endpoint. Tests replace it so they never dial Chrome.
var liveCDPSweep = livecdp.SweepStaleBlankTabs

// liveCDPSweepMaxPasses caps the startup sweep. Each pass closes every
// stale tab it can within its budget, so a pile needs more than one pass
// only when a pass runs out of time; a cap stops a browser that keeps
// growing blank tabs from holding the sweep open forever.
const liveCDPSweepMaxPasses = 10

// liveCDPSweepPassBudget is one sweep pass's time: the age wait plus room
// to close a few thousand paced tabs.
const liveCDPSweepPassBudget = livecdp.DefaultSweepMinAge + 2*time.Minute

// startLiveCDPSweep closes, in the background, the about:blank tabs earlier
// sink versions leaked into the live CDP Chrome (one per inject). It runs
// sweep passes, each with its own budget, until one closes nothing, so a
// pile of thousands clears on one sink start. It runs when live CDP is on
// and the sink may modify Chrome (not --dry-run). It reports whether it
// started.
func startLiveCDPSweep(ctx context.Context, cfg *config.SinkConfig) bool {
	if !cfg.LiveCDP.Enabled || sinkDryRun {
		return false
	}
	endpoint := cfg.LiveCDP.Endpoint
	if endpoint == "" {
		endpoint = livecdp.DefaultCDPEndpoint
	}
	go runLiveCDPSweep(ctx, endpoint)
	return true
}

// runLiveCDPSweep runs sweep passes until one closes nothing, a pass fails
// without closing anything, ctx ends, or the pass cap is reached.
func runLiveCDPSweep(ctx context.Context, endpoint string) {
	total := 0
	for pass := 1; pass <= liveCDPSweepMaxPasses && ctx.Err() == nil; pass++ {
		passCtx, cancel := context.WithTimeout(ctx, liveCDPSweepPassBudget)
		closed, err := liveCDPSweep(passCtx, endpoint, livecdp.DefaultSweepMinAge)
		cancel()
		total += closed
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie sink: stale-tab sweep pass %d at %s: closed %d, then failed: %v\n", pass, endpoint, closed, err)
		}
		if closed == 0 {
			break
		}
	}
	if total > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie sink: stale-tab sweep closed %d about:blank tab(s) at %s\n", total, endpoint)
	}
}
