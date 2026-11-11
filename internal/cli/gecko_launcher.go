package cli

import (
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko/launcher"
	"github.com/spf13/cobra"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var geckoLauncherBrowser, geckoLauncherProfile, geckoLauncherApp string
var geckoLauncherPort int
var geckoLauncherCmd = &cobra.Command{Use: "gecko-launcher", Short: "Opt-in Gecko launcher for the live source"}
var geckoLauncherInstallCmd = &cobra.Command{Use: "install", Short: "Write a launcher with BiDi enabled; does not start or restart the browser", RunE: func(cmd *cobra.Command, args []string) error {
	cfg, e := config.LoadSourceLocalWithOverrides(common.ConfigDir, config.SourceOverrides{Browser: geckoLauncherBrowser, Profile: geckoLauncherProfile})
	if e != nil {
		return e
	}
	if !isGeckoSource(cfg) {
		return fmt.Errorf("choose --browser zen, firefox, librewolf or floorp")
	}
	if geckoLauncherApp != "" {
		cfg.Browser.AppPath = geckoLauncherApp
	}
	identity, e := resolveGeckoProfile(cfg)
	if e != nil {
		return e
	}
	b, e := browsers.Lookup(cfg.Browser.Name)
	if e != nil {
		return e
	}
	app := cfg.Browser.AppPath
	if app == "" {
		if runtime.GOOS == "darwin" {
			app = b.AppBundle
		} else {
			app, e = exec.LookPath(b.Name)
			if e != nil {
				return e
			}
		}
	}
	if st, e := os.Stat(app); e == nil && st.IsDir() {
		if runtime.GOOS == "darwin" {
			app = filepath.Join(app, "Contents", "MacOS", b.Name)
		} else {
			app = filepath.Join(app, b.Name)
		}
	}
	app, e = filepath.Abs(app)
	if e != nil {
		return e
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	port := cfg.Browser.BiDiPort
	if geckoLauncherPort != 0 {
		port = geckoLauncherPort
	}
	path, e := launcher.Install(launcher.Options{Home: home, GOOS: runtime.GOOS, Name: b.Name, Executable: app, Profile: identity.Dir, Port: port})
	if e != nil {
		return e
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Installed %s. Quit the browser yourself, then open this launcher. The loopback BiDi port permits same-user browser control.\n", path)
	return nil
}}

func init() {
	f := geckoLauncherInstallCmd.Flags()
	f.StringVar(&geckoLauncherBrowser, "browser", "", "Gecko browser")
	f.StringVar(&geckoLauncherProfile, "profile", "", "profile name or absolute directory")
	f.StringVar(&geckoLauncherApp, "app-path", "", "browser app bundle or executable")
	f.IntVar(&geckoLauncherPort, "bidi-port", 0, "loopback BiDi port")
	geckoLauncherCmd.AddCommand(geckoLauncherInstallCmd)
	rootCmd.AddCommand(geckoLauncherCmd)
}
