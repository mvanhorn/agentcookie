package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/state"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local config, cookie policy, live daemon state, and any load errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		st := struct {
			Version      string               `json:"version"`
			ConfigDir    string               `json:"config_dir"`
			SourceConfig *config.SourceConfig `json:"source_config,omitempty"`
			SinkConfig   *config.SinkConfig   `json:"sink_config,omitempty"`
			Blocklist    *config.Blocklist    `json:"blocklist,omitempty"`
			CookiePolicy string               `json:"cookie_policy,omitempty"`
			SourceState  *state.SourceState   `json:"source_state,omitempty"`
			SinkState    *state.SinkState     `json:"sink_state,omitempty"`
			Errors       []string             `json:"errors,omitempty"`
		}{
			Version:   Version,
			ConfigDir: common.ConfigDir,
		}

		if s, err := config.LoadSource(common.ConfigDir); err == nil {
			st.SourceConfig = s
		} else {
			st.Errors = append(st.Errors, "source.yaml: "+err.Error())
		}
		if s, err := config.LoadSink(common.ConfigDir); err == nil {
			st.SinkConfig = s
		} else {
			st.Errors = append(st.Errors, "sink.yaml: "+err.Error())
		}
		if bl, err := config.LoadBlocklist(common.ConfigDir); err == nil {
			st.Blocklist = bl
			st.CookiePolicy = bl.CookiePolicySummary()
		} else {
			st.Errors = append(st.Errors, "blocklist.yaml: "+err.Error())
		}
		if ss, err := state.LoadSource(state.SourcePath(home)); err == nil && ss != nil {
			st.SourceState = ss
		}
		if sk, err := state.LoadSink(state.SinkPath(home)); err == nil && sk != nil {
			st.SinkState = sk
		}

		if common.JSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		}

		fmt.Printf("agentcookie %s\n", st.Version)
		fmt.Printf("config dir: %s\n", st.ConfigDir)
		if st.SourceConfig != nil {
			fmt.Printf("  source -> %s\n", st.SourceConfig.Sink.URL)
			fmt.Printf("    chrome db: %s\n", st.SourceConfig.Chrome.DBPath)
		} else {
			fmt.Println("  source: not configured")
		}
		if st.SinkConfig != nil {
			fmt.Printf("  sink listening on %s\n", st.SinkConfig.Listen.Addr)
			if st.SinkConfig.Chrome.DBPath != "" {
				fmt.Printf("    chrome db: %s\n", st.SinkConfig.Chrome.DBPath)
			}
		} else {
			fmt.Println("  sink: not configured")
		}
		if st.Blocklist != nil {
			fmt.Printf("  cookie policy: %s\n", st.CookiePolicy)
			fmt.Printf("  blocklist.yaml v%d: %d patterns\n", st.Blocklist.Version, len(st.Blocklist.Domains))
			for _, d := range st.Blocklist.Domains {
				if d.Description != "" {
					fmt.Printf("    - %s  (%s)\n", d.Pattern, d.Description)
				} else {
					fmt.Printf("    - %s\n", d.Pattern)
				}
			}
		} else {
			fmt.Println("  cookie policy: not configured")
		}
		if st.SourceState != nil {
			ago := "never"
			if !st.SourceState.LastPush.IsZero() {
				ago = time.Since(st.SourceState.LastPush).Round(time.Second).String() + " ago"
			}
			fmt.Printf("  source daemon: %d pushes, %d failures, last push %s\n",
				st.SourceState.TotalPushes, st.SourceState.TotalFailures, ago)
		}
		if st.SinkState != nil {
			ago := "never"
			if !st.SinkState.LastWrite.IsZero() {
				ago = time.Since(st.SinkState.LastWrite).Round(time.Second).String() + " ago"
			}
			fmt.Printf("  sink daemon: %d writes via %s, %d rejected, last write %s\n",
				st.SinkState.TotalWrites, st.SinkState.LastWriteMode, st.SinkState.TotalRejects, ago)
			if n := len(st.SinkState.LastAdapterResults); n > 0 {
				ok, skipped, failed := 0, 0, 0
				for _, r := range st.SinkState.LastAdapterResults {
					switch {
					case r.Err != "":
						failed++
					case r.Skipped:
						skipped++
					default:
						ok++
					}
				}
				fmt.Printf("    adapters (last run): %d ok, %d skipped, %d failed (of %d)\n", ok, skipped, failed, n)
			}
		}
		for _, e := range st.Errors {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", e)
		}
		return nil
	},
}
