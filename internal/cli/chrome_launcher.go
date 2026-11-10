package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/onboarding"
	"github.com/spf13/cobra"
)

var chromeLauncherName, chromeLauncherPath string
var chromeLauncherCmd = &cobra.Command{Use: "chrome-launcher", Short: "Create the readiness-gated owned Chrome app"}

func init() {
	install := &cobra.Command{Use: "install", Short: "Write agentcookie Chrome.app without starting a browser", RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("chrome app launcher is macOS-only; use agentcookie destination open on this platform")
		}
		cfg, err := config.LoadSink(common.ConfigDir)
		if err != nil {
			return err
		}
		ds, err := cfg.ResolveDestinations()
		if err != nil {
			return err
		}
		found := false
		for _, d := range ds {
			if d.Name == chromeLauncherName && d.Kind == "live_chrome" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("named live_chrome destination %q not configured", chromeLauncherName)
		}
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		dir, err := filepath.Abs(common.ConfigDir)
		if err != nil {
			return err
		}
		path := chromeLauncherPath
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			path = filepath.Join(home, "Applications", "agentcookie Chrome.app")
		}
		if err := onboarding.InstallChromeApp(path, bin, dir, chromeLauncherName); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Start the sink, then open this app; pending/repair state blocks its first window.\n", path)
		return nil
	}}
	install.Flags().StringVar(&chromeLauncherName, "destination", "live_chrome", "configured owned destination name")
	install.Flags().StringVar(&chromeLauncherPath, "output", "", "absolute .app path (default ~/Applications/agentcookie Chrome.app)")
	chromeLauncherCmd.AddCommand(install)
	rootCmd.AddCommand(chromeLauncherCmd)
}
