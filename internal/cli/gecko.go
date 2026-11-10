package cli

import (
	"github.com/mvanhorn/agentcookie/internal/gecko/broker"
	"github.com/spf13/cobra"
)

var geckoBrokerOptions broker.Options
var geckoBrokerCmd = &cobra.Command{Use: "gecko-broker", Hidden: true, Short: "Own a Gecko BiDi session across daemon restarts", RunE: func(cmd *cobra.Command, args []string) error { return broker.Run(cmd.Context(), geckoBrokerOptions) }}

func init() {
	f := geckoBrokerCmd.Flags()
	f.StringVar(&geckoBrokerOptions.AssetsPath, "assets-file", "", "private embedded companion assets")
	f.StringVar(&geckoBrokerOptions.Profile, "profile", "", "canonical throwaway or configured profile directory")
	f.StringVar(&geckoBrokerOptions.Endpoint, "endpoint", "", "loopback BiDi endpoint")
	f.StringVar(&geckoBrokerOptions.StateDir, "state-dir", "", "private state directory")
	f.IntVar(&geckoBrokerOptions.CompanionPort, "companion-port", 9225, "companion loopback port")
	_ = geckoBrokerCmd.MarkFlagRequired("profile")
	_ = geckoBrokerCmd.MarkFlagRequired("endpoint")
	_ = geckoBrokerCmd.MarkFlagRequired("state-dir")
	rootCmd.AddCommand(geckoBrokerCmd)
}
