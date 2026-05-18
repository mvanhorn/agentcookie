package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/state"
)

var verifyAdaptersCmd = &cobra.Command{
	Use:   "verify-adapters",
	Short: "Report the most recent sinkpush adapter results from sink-state",
	Long: `verify-adapters reads ~/.agentcookie/sink-state.json and prints one row
per registered v0.11 sinkpush adapter, showing whether the most recent
sync's post-write push to each PP CLI's session cache succeeded.

Use this after running 'agentcookie source --once' to confirm the
five built-in adapters (instacart-pp-cli, airbnb-pp-cli, ebay-pp-cli,
pagliacci-pp-cli, table-reservation-goat-pp-cli) all wrote without
error. Failures here mean the corresponding PP CLI will fall back to
its native Keychain auth path on next invocation -- which is exactly
the prompt-spam the adapter pattern exists to avoid.

Returns exit code 0 on success or no-state-yet. Returns exit code 1
when at least one adapter result reported an error (informational; the
sink itself does not abort on adapter failures).

Use --json with the parent agentcookie --json flag to emit a
structured result usable by SSH agents.`,
	RunE: runVerifyAdapters,
}

func init() {
	wizardCmd.AddCommand(verifyAdaptersCmd)
}

func runVerifyAdapters(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()
	sk, err := state.LoadSink(state.SinkPath(home))
	if err != nil {
		return fmt.Errorf("load sink-state.json: %w", err)
	}
	if sk == nil {
		if common.JSON {
			return emitVerifyJSON(nil, "no_state")
		}
		fmt.Println("no sink-state.json yet -- run 'agentcookie source --once' from the source machine first")
		return nil
	}
	if len(sk.LastAdapterResults) == 0 {
		if common.JSON {
			return emitVerifyJSON(nil, "no_runs")
		}
		fmt.Println("no adapter runs recorded yet -- this sink may be running an agentcookie release older than v0.11, or no sync has hit it post-upgrade")
		return nil
	}

	if common.JSON {
		return emitVerifyJSON(sk.LastAdapterResults, "ok")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ADAPTER\tSTATUS\tPUSHED\tDETAIL")
	fmt.Fprintln(tw, "-------\t------\t------\t------")
	failed := 0
	for _, r := range sk.LastAdapterResults {
		status, detail := formatAdapterRow(r)
		if r.Err != "" {
			failed++
		}
		pushed := ""
		if r.Pushed > 0 {
			pushed = fmt.Sprintf("%d", r.Pushed)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, status, pushed, detail)
	}
	_ = tw.Flush()

	if len(sk.LastAdapterResults) > 0 {
		ago := time.Since(sk.LastAdapterResults[0].RanAt).Round(time.Second)
		fmt.Printf("\nlast run: %s ago\n", ago)
	}
	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// formatAdapterRow returns the table's STATUS and DETAIL columns for
// one adapter result. The two values share a small switch so the
// status label and human-readable detail line stay in sync.
func formatAdapterRow(r state.AdapterResult) (status, detail string) {
	switch {
	case r.Err != "":
		return "FAIL", r.Err
	case r.Skipped:
		return "skip", r.SkippedReason
	default:
		return "ok", ""
	}
}

// emitVerifyJSON serializes the verify-adapters output as a JSON
// envelope that SSH agents and CI can parse without screen-scraping
// the table.
func emitVerifyJSON(results []state.AdapterResult, statusCode string) error {
	envelope := struct {
		Status  string                 `json:"status"`
		Results []state.AdapterResult  `json:"results,omitempty"`
	}{
		Status:  statusCode,
		Results: results,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(envelope)
}
