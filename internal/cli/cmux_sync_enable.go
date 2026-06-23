package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/cmuxconfig"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

// Seams so tests can exercise enable/disable without touching the real
// cmux.json or launchd.
var (
	cmuxSyncSetMode        = cmuxconfig.SetSocketControlMode
	cmuxSyncInstallAgent   = installLaunchAgent
	cmuxSyncUninstallAgent = launchd.Uninstall
	// cmuxSyncKeychainCheck is the Keychain pre-flight seam. It verifies that
	// the binary can read Chrome Safe Storage before the persistent agent is
	// installed, so the KeepAlive restart loop never starts.
	cmuxSyncKeychainCheck = func(b chrome.Browser) error {
		_, err := chrome.SafeStoragePasswordFor(b)
		return err
	}
	// cmuxSyncConfigExists reports whether cmux's config file is present. It lets
	// enableCmuxLoop run the Keychain pre-flight BEFORE mutating cmux.json while
	// still surfacing the "launch cmux once" no-op first -- a stubbable seam so
	// tests don't depend on a real cmux.json on disk.
	cmuxSyncConfigExists = func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}
)

var cmuxSyncEnableCmuxPath string

var cmuxSyncEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Make the cmux local loop persistent: install a launch agent and open cmux's socket",
	Long: `enable sets up the local loop to run automatically: it points cmux's
socketControlMode at allowAll (so the launchd agent, which is not a cmux
child, can reach the control socket), installs a LaunchAgent that runs
` + "`cmux-sync --watch`" + ` over your full cookie set, and tells you to restart
cmux once (the mode is read only at app launch).

If cmux is not installed, enable is a clean no-op. Idempotent.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return enableCmuxLoop(cmuxSyncEnableCmuxPath, false)
	},
}

var cmuxSyncDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove the cmux local-loop launch agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cmuxSyncUninstallAgent(launchd.Spec{Role: launchd.RoleCmuxSync}); err != nil {
			return fmt.Errorf("remove cmux-sync agent: %w", err)
		}
		fmt.Fprintln(os.Stderr, "agentcookie cmux-sync: removed launch agent dev.agentcookie.cmux-sync")
		fmt.Fprintln(os.Stderr, "agentcookie cmux-sync: to revert cmux's socket, set automation.socketControlMode back to \"cmuxOnly\" in ~/.config/cmux/cmux.json and restart cmux")
		return nil
	},
}

func init() {
	cmuxSyncEnableCmd.Flags().StringVar(&cmuxSyncEnableCmuxPath, "cmux-path", "", "override the cmux CLI path (default: the app bundle, then PATH)")
	cmuxSyncCmd.AddCommand(cmuxSyncEnableCmd)
	cmuxSyncCmd.AddCommand(cmuxSyncDisableCmd)
}

// cmuxAutoEnable is the function the wizard calls to wire the loop. A
// package var so tests can spy on it.
var cmuxAutoEnable = enableCmuxLoop

// maybeAutoEnableCmux runs the default-on wiring at the tail of
// `wizard install`, unless --no-cmux was passed. Non-fatal: a wiring
// hiccup logs and is swallowed so it never fails the install.
func maybeAutoEnableCmux() {
	if wizardNoCmux {
		return
	}
	if err := cmuxAutoEnable("", false); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: cmux local-loop setup failed (non-fatal): %v\n", err)
	}
}

// enableCmuxLoop configures cmux's socket and installs the cmux-sync launch
// agent. It is a clean no-op when cmux (or its config) is absent, so the
// wizard can call it unconditionally. quiet suppresses the per-step lines
// (the wizard prints its own summary).
func enableCmuxLoop(cmuxPath string, quiet bool) error {
	logf := func(format string, a ...any) {
		if !quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	binary := sinkpush.ResolveCmuxBinary(cmuxPath)
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		logf("agentcookie cmux-sync: cmux not found at %s; skipping local-loop setup", binary)
		return nil
	}

	cfgPath, err := cmuxconfig.DefaultConfigPath()
	if err != nil {
		return err
	}

	// cmux.json-missing no-op: surface "launch cmux once" before anything else
	// so a never-launched cmux gets the relevant remediation, not a Keychain one.
	if !cmuxSyncConfigExists(cfgPath) {
		logf("agentcookie cmux-sync: cmux is installed but %s is missing; launch cmux once, then re-run `agentcookie cmux-sync enable`", cfgPath)
		return nil
	}

	// Keychain pre-flight runs BEFORE mutating cmux.json. A go install binary is
	// ad-hoc signed and always needs a first-run grant; failing here prevents the
	// KeepAlive restart loop before it starts AND leaves cmux.json untouched (no
	// orphaned socketControlMode=allowAll on an aborted enable). It runs after the
	// cmux-absent / cmux.json-missing no-ops so those surface first.
	defaultBrowser, _ := chrome.LookupBrowser("")
	if err := cmuxSyncKeychainCheck(defaultBrowser); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie cmux-sync enable: Keychain pre-flight failed — %v\n", err)
		fmt.Fprintf(os.Stderr, "agentcookie cmux-sync enable: fix first: %s\n", chrome.SafeStorageRemediation)
		return fmt.Errorf("cmux-sync enable: Keychain not accessible; run `agentcookie wizard set-keychain-access` first")
	}

	bak, err := cmuxSyncSetMode(cfgPath, "allowAll", "", time.Now())
	if errors.Is(err, cmuxconfig.ErrNotFound) {
		// cmux.json vanished between the existence check and the write (TOCTOU);
		// no mutation happened, so this is still a clean no-op.
		logf("agentcookie cmux-sync: cmux is installed but %s is missing; launch cmux once, then re-run `agentcookie cmux-sync enable`", cfgPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("set cmux socketControlMode: %w", err)
	}
	logf("agentcookie cmux-sync: set socketControlMode=allowAll in %s (backup: %s)", cfgPath, bak)

	binPath, err := os.Executable()
	if err != nil {
		return err
	}
	binPath, _ = filepath.Abs(binPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locate home dir: %w", err)
	}
	logDir := filepath.Join(home, ".agentcookie", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	if err := cmuxSyncInstallAgent(launchd.Spec{
		Role:       launchd.RoleCmuxSync,
		BinaryPath: binPath,
		LogDir:     logDir,
		ExtraArgs:  []string{"--watch"},
	}); err != nil {
		return fmt.Errorf("install cmux-sync launch agent: %w", err)
	}
	logf("agentcookie cmux-sync: installed launch agent dev.agentcookie.cmux-sync (runs cmux-sync --watch over all cookies)")
	logf("agentcookie cmux-sync: RESTART cmux once to apply socketControlMode (read only at app launch). Verify with: agentcookie doctor")
	return nil
}
