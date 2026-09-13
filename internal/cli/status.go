package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// ChromeStoreInfo represents a discovered Chrome profile store for status output.
type ChromeStoreInfo struct {
	Browser     string `json:"browser"`
	Profile     string `json:"profile"`
	CookiesPath string `json:"cookies_path"`
	IsDefault   bool   `json:"is_default"`
}

// ChromeStoresStatus summarizes discovered Chrome stores for status output.
type ChromeStoresStatus struct {
	Stores       []ChromeStoreInfo `json:"stores"`
	SkippedCount int               `json:"skipped_count"`
	CanDecrypt   bool              `json:"can_decrypt"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print local config, cookie policy, live daemon state, discovered Chrome stores, and any load errors",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		st := struct {
			Version      string               `json:"version"`
			ConfigDir    string               `json:"config_dir"`
			SourceConfig *config.SourceConfig `json:"source_config,omitempty"`
			SinkConfig   *config.SinkConfig   `json:"sink_config,omitempty"`
			Blocklist    *config.Blocklist    `json:"blocklist,omitempty"`
			CookiePolicy string               `json:"cookie_policy,omitempty"`
			ChromeStores *ChromeStoresStatus  `json:"chrome_stores,omitempty"`
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
		if ss, err := state.LoadSource(sourceStatePathForConfig(st.SourceConfig, common.ConfigDir, home)); err == nil && ss != nil {
			st.SourceState = ss
		}
		if sk, err := state.LoadSink(state.SinkPath(home)); err == nil && sk != nil {
			st.SinkState = sk
		}

		// Discover Chrome stores. Include config's CDP.ProfileDir if set.
		cdpProfileDir := ""
		if st.SinkConfig != nil {
			cdpProfileDir = st.SinkConfig.CDP.ProfileDir
		}
		discovery := chromepaths.DiscoverForConfig(cdpProfileDir)
		if len(discovery.Stores) > 0 || len(discovery.Skipped) > 0 {
			stores := make([]ChromeStoreInfo, 0, len(discovery.Stores))
			for _, s := range discovery.Stores {
				stores = append(stores, ChromeStoreInfo{
					Browser:     s.Browser,
					Profile:     s.Profile,
					CookiesPath: s.CookiesPath,
					IsDefault:   s.IsDefault,
				})
			}
			st.ChromeStores = &ChromeStoresStatus{
				Stores:       stores,
				SkippedCount: len(discovery.Skipped),
				CanDecrypt:   runtime.GOOS == "darwin",
			}
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
			// Per-sink breakdown (multi-sink fan-out). Absent for a
			// single legacy sink or an old state file, where the
			// aggregate line above is the whole story.
			for _, sink := range st.SourceState.Sinks {
				label := sink.URL
				if sink.Peer != "" {
					label = fmt.Sprintf("%s (%s)", sink.Peer, sink.URL)
				}
				sinkAgo := "never"
				if !sink.LastPush.IsZero() {
					sinkAgo = time.Since(sink.LastPush).Round(time.Second).String() + " ago"
				}
				fmt.Printf("    sink %s: %d pushes, %d failures, last push %s\n",
					label, sink.TotalPushes, sink.TotalFailures, sinkAgo)
				if sink.LastError != "" {
					fmt.Printf("      last error: %s\n", sink.LastError)
				}
			}
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
			if cdp := st.SinkState.LiveCDP; cdp != nil && cdp.Enabled {
				cdpAgo := "never"
				if !cdp.LastInjectAt.IsZero() {
					cdpAgo = time.Since(cdp.LastInjectAt).Round(time.Second).String() + " ago"
				}
				fmt.Printf("    live_cdp: endpoint=%s, %d injects, %d failures, last inject %s\n",
					cdp.Endpoint, cdp.TotalInjects, cdp.TotalFailures, cdpAgo)
				if cdp.LastCookies > 0 || cdp.LastContexts > 0 {
					fmt.Printf("      last inject: %d cookies into %d context(s)\n", cdp.LastCookies, cdp.LastContexts)
				}
				if cdp.LastError != "" {
					fmt.Printf("      last error: %s\n", cdp.LastError)
				}
			}
		}
		if st.ChromeStores != nil && len(st.ChromeStores.Stores) > 0 {
			decryptNote := ""
			if !st.ChromeStores.CanDecrypt {
				decryptNote = " (decrypt not supported on Linux)"
			}
			fmt.Printf("  chrome stores: %d discovered%s\n", len(st.ChromeStores.Stores), decryptNote)
			for _, s := range st.ChromeStores.Stores {
				label := s.Browser + "/" + s.Profile
				if s.IsDefault {
					label += " (default)"
				}
				fmt.Printf("    - %s\n", label)
			}
			if st.ChromeStores.SkippedCount > 0 {
				fmt.Printf("    (%d profile(s) skipped - no Cookies file)\n", st.ChromeStores.SkippedCount)
			}
		}
		for _, e := range st.Errors {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", e)
		}
		return nil
	},
}
