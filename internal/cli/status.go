package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local config, allowlist, and any load errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		state := struct {
			Version      string                `json:"version"`
			ConfigDir    string                `json:"config_dir"`
			SourceConfig *config.SourceConfig  `json:"source_config,omitempty"`
			SinkConfig   *config.SinkConfig    `json:"sink_config,omitempty"`
			Allowlist    *config.Allowlist     `json:"allowlist,omitempty"`
			Errors       []string              `json:"errors,omitempty"`
		}{
			Version:   Version,
			ConfigDir: common.ConfigDir,
		}

		if s, err := config.LoadSource(common.ConfigDir); err == nil {
			state.SourceConfig = s
		} else {
			state.Errors = append(state.Errors, "source.yaml: "+err.Error())
		}
		if s, err := config.LoadSink(common.ConfigDir); err == nil {
			state.SinkConfig = s
		} else {
			state.Errors = append(state.Errors, "sink.yaml: "+err.Error())
		}
		if a, err := config.LoadAllowlist(common.ConfigDir); err == nil {
			state.Allowlist = a
		} else {
			state.Errors = append(state.Errors, "allowlist.yaml: "+err.Error())
		}

		if common.JSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(state)
		}

		fmt.Printf("agentcookie %s\n", state.Version)
		fmt.Printf("config dir: %s\n", state.ConfigDir)
		if state.SourceConfig != nil {
			fmt.Printf("  source -> %s\n", state.SourceConfig.Sink.URL)
			fmt.Printf("    chrome db: %s\n", state.SourceConfig.Chrome.DBPath)
		} else {
			fmt.Println("  source: not configured")
		}
		if state.SinkConfig != nil {
			fmt.Printf("  sink listening on %s\n", state.SinkConfig.Listen.Addr)
			fmt.Printf("    chrome db: %s\n", state.SinkConfig.Chrome.DBPath)
		} else {
			fmt.Println("  sink: not configured")
		}
		if state.Allowlist != nil {
			fmt.Printf("  allowlist v%d: %d domains\n", state.Allowlist.Version, len(state.Allowlist.Domains))
			for _, d := range state.Allowlist.Domains {
				if d.Description != "" {
					fmt.Printf("    - %s  (%s)\n", d.Pattern, d.Description)
				} else {
					fmt.Printf("    - %s\n", d.Pattern)
				}
			}
		} else {
			fmt.Println("  allowlist: not configured")
		}
		for _, e := range state.Errors {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", e)
		}
		return nil
	},
}
