package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromectl"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/notify"
)

const (
	keepaliveTick       = 15 * time.Second
	keepaliveGateEvery  = time.Minute
	keepaliveRetryAfter = 60 * time.Second
	// keepaliveSendDeadline stays well under the ~20s after which Chrome
	// kills a browser that does not answer a handoff; a healthy handoff
	// takes a few seconds at most.
	keepaliveSendDeadline = 10 * time.Second
	keepaliveNotifyAfter  = 3
)

var profileKeepaliveCmd = &cobra.Command{
	Use:   "profile-keepalive",
	Short: "Keep each Chrome profile paired with a different Claude account loaded, so Claude in Chrome stays connected",
	Long: `Claude in Chrome pairs only with the Claude account it is signed into.
With one Chrome profile per Claude account, a profile whose last window is
closed unloads, and Claude in Chrome stops working for that account.

profile-keepalive runs in the background and loads such a profile again
inside the running Chrome, with no window and no focus change. It does
nothing unless two or more profiles are signed into different Claude
accounts, never starts or restarts Chrome, and waits while Chrome is not
running.

While it is on, closing a profile's windows does not cut Claude in
Chrome's access to that profile. Only ` + "`profile-keepalive disable`" + ` does.`,
}

var profileKeepaliveRunCmd = &cobra.Command{
	Use:    "run",
	Short:  "Run the keep-alive loop (started by the launch agent)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if runtime.GOOS != "darwin" {
			return errors.New("profile-keepalive: macOS only")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		chrome.CleanupClaudeExtTemp()
		return liveKeepaliveLoop().run(ctx)
	},
}

var profileKeepaliveEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Install the keep-alive launch agent (no-op unless profiles span two or more Claude accounts)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return enableProfileKeepalive(false, false)
	},
}

var profileKeepaliveDisableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Remove the keep-alive launch agent and keep it off",
	RunE: func(cmd *cobra.Command, args []string) error {
		return disableProfileKeepalive()
	},
}

var (
	keepalivePauseProfile string
	keepalivePauseFor     time.Duration
)

var profileKeepalivePauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "Stop reloading one profile for a while (run this before profile-sync)",
	RunE: func(cmd *cobra.Command, args []string) error {
		until := time.Now().Add(keepalivePauseFor)
		if err := writeKeepalivePause(keepalivePauseProfile, until); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "agentcookie profile-keepalive: %q will not be reloaded until %s\n", keepalivePauseProfile, until.Format(time.Kitchen))
		return nil
	},
}

func init() {
	profileKeepalivePauseCmd.Flags().StringVar(&keepalivePauseProfile, "profile", "", `Chrome profile folder to pause, e.g. "Profile 1"`)
	profileKeepalivePauseCmd.Flags().DurationVar(&keepalivePauseFor, "for", 10*time.Minute, "how long to pause")
	_ = profileKeepalivePauseCmd.MarkFlagRequired("profile")
	profileKeepaliveCmd.AddCommand(profileKeepaliveRunCmd, profileKeepaliveEnableCmd, profileKeepaliveDisableCmd, profileKeepalivePauseCmd)
}

// keepaliveDeps are the outside calls the loop makes.
type keepaliveDeps struct {
	Root        string
	Qualify     func(root string) ([]chrome.ClaudeProfile, []error)
	ChromePID   func(root string) (int, bool)
	Loaded      func(pid int, root, profile string) bool
	Load        func(ctx context.Context, profile string) error
	Notify      func(title, body string)
	Now         func() time.Time
	PausedUntil func(profile string) time.Time
	Logf        func(format string, a ...any)
}

type keepaliveProfileState struct {
	awaitingVerdict bool
	sentAt          time.Time
	failures        int
	notified        bool
	lastFailed      time.Time
}

type keepaliveLoop struct {
	d        keepaliveDeps
	gate     []chrome.ClaudeProfile
	gateAt   time.Time
	idleLogd bool
	state    map[string]*keepaliveProfileState
}

func newKeepaliveLoop(d keepaliveDeps) *keepaliveLoop {
	return &keepaliveLoop{d: d, state: map[string]*keepaliveProfileState{}}
}

func liveKeepaliveLoop() *keepaliveLoop {
	return newKeepaliveLoop(keepaliveDeps{
		Root:      chromepaths.ChromeProfileRoot(),
		Qualify:   chrome.QualifyingClaudeProfiles,
		ChromePID: chromectl.RunningChromePID,
		Loaded:    chromectl.ProfileLoaded,
		Load: func(ctx context.Context, profile string) error {
			return chromectl.LoadProfileWindowless(ctx, profile, keepaliveSendDeadline)
		},
		Notify: func(title, body string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = notify.Show(ctx, title, body)
		},
		Now:         time.Now,
		PausedUntil: keepalivePausedUntil,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, time.Now().Format(time.RFC3339)+" profile-keepalive: "+format+"\n", a...)
		},
	})
}

func (l *keepaliveLoop) run(ctx context.Context) error {
	t := time.NewTicker(keepaliveTick)
	defer t.Stop()
	for {
		l.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (l *keepaliveLoop) tick(ctx context.Context) {
	now := l.d.Now()
	if l.gateAt.IsZero() || now.Sub(l.gateAt) >= keepaliveGateEvery {
		gate, errs := l.d.Qualify(l.d.Root)
		for _, err := range errs {
			l.d.Logf("skipped a profile: %v", err)
		}
		l.gate, l.gateAt = gate, now
	}
	if len(l.gate) == 0 {
		if !l.idleLogd {
			l.d.Logf("idle: fewer than two Chrome profiles are signed into different Claude accounts")
			l.idleLogd = true
		}
		if len(l.state) > 0 {
			l.state = map[string]*keepaliveProfileState{}
		}
		return
	}
	l.idleLogd = false

	pid, ok := l.d.ChromePID(l.d.Root)
	if !ok {
		return
	}
	for _, p := range l.gate {
		st := l.state[p.Profile]
		if st == nil {
			st = &keepaliveProfileState{}
			l.state[p.Profile] = st
		}
		if l.d.Loaded(pid, l.d.Root, p.Profile) {
			if st.failures > 0 || st.awaitingVerdict {
				l.d.Logf("%s is loaded", p.Profile)
			}
			*st = keepaliveProfileState{}
			continue
		}
		if st.awaitingVerdict {
			st.awaitingVerdict = false
			l.fail(st, p.Profile, st.sentAt, "it was still not loaded after a load")
		}
		if !st.lastFailed.IsZero() && now.Sub(st.lastFailed) < keepaliveRetryAfter {
			continue
		}
		if now.Before(l.d.PausedUntil(p.Profile)) {
			continue
		}
		// Chrome may have quit since the check above; a send with no
		// browser to hand off to would start one.
		if again, ok := l.d.ChromePID(l.d.Root); !ok || again != pid {
			return
		}
		l.d.Logf("loading %s", p.Profile)
		if err := l.d.Load(ctx, p.Profile); err != nil {
			l.fail(st, p.Profile, now, err.Error())
			continue
		}
		st.awaitingVerdict, st.sentAt = true, now
	}
}

func (l *keepaliveLoop) fail(st *keepaliveProfileState, profile string, sentAt time.Time, why string) {
	st.failures++
	st.lastFailed = sentAt
	l.d.Logf("could not load %s (%d in a row): %s", profile, st.failures, why)
	if st.failures >= keepaliveNotifyAfter && !st.notified {
		st.notified = true
		l.d.Notify("agentcookie", fmt.Sprintf("Chrome profile %q will not stay loaded, so Claude in Chrome is disconnected for its account. Run `agentcookie doctor`.", profile))
	}
}

func keepaliveStateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentcookie", "profile-keepalive")
}

func keepalivePausePath(profile string) string {
	return filepath.Join(keepaliveStateDir(), "pause-"+strings.ReplaceAll(profile, " ", "-")+".json")
}

type keepalivePause struct {
	Until time.Time `json:"until"`
}

func writeKeepalivePause(profile string, until time.Time) error {
	if !chrome.IsProfileDirName(profile) {
		return fmt.Errorf("profile-keepalive: %q is not a Chrome profile folder (want Default or \"Profile N\")", profile)
	}
	if err := os.MkdirAll(keepaliveStateDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(keepalivePause{Until: until})
	if err != nil {
		return err
	}
	tmp := keepalivePausePath(profile) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, keepalivePausePath(profile))
}

func keepalivePausedUntil(profile string) time.Time {
	b, err := os.ReadFile(keepalivePausePath(profile))
	if err != nil {
		return time.Time{}
	}
	var p keepalivePause
	if json.Unmarshal(b, &p) != nil {
		return time.Time{}
	}
	return p.Until
}

func keepaliveOptedOutPath() string { return filepath.Join(keepaliveStateDir(), "opted-out") }

func keepaliveOptedOut() bool {
	_, err := os.Lstat(keepaliveOptedOutPath())
	return err == nil
}

var (
	keepaliveQualify        = chrome.QualifyingClaudeProfiles
	keepaliveInstallAgent   = installLaunchAgent
	keepaliveUninstallAgent = launchd.Uninstall
	keepaliveAutoEnable     = enableProfileKeepalive
	// keepaliveForceOS lets tests on Linux CI run the macOS-only paths.
	keepaliveForceOS = false
)

// maybeAutoEnableProfileKeepalive runs at the tail of `wizard install`. It
// is skipped by --no-profile-keepalive or an earlier disable, and never
// fails the install.
func maybeAutoEnableProfileKeepalive() {
	if wizardNoProfileKeepalive || keepaliveOptedOut() || (runtime.GOOS != "darwin" && !keepaliveForceOS) {
		return
	}
	if err := keepaliveAutoEnable(true, false); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie wizard: profile keep-alive setup failed (non-fatal): %v\n", err)
	}
}

// enableProfileKeepalive installs the launch agent when two or more Chrome
// profiles are signed into different Claude accounts, and is a clean no-op
// otherwise. An explicit enable clears an earlier opt-out; the wizard's
// call (fromWizard) does not.
func enableProfileKeepalive(fromWizard, quiet bool) error {
	logf := func(format string, a ...any) {
		if !quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}
	if runtime.GOOS != "darwin" && !keepaliveForceOS {
		logf("agentcookie profile-keepalive: macOS only; nothing to do")
		return nil
	}
	if !fromWizard {
		if err := os.Remove(keepaliveOptedOutPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	profiles, _ := keepaliveQualify(chromepaths.ChromeProfileRoot())
	if len(profiles) == 0 {
		logf("agentcookie profile-keepalive: not needed (fewer than two Chrome profiles are signed into different Claude accounts in Claude in Chrome)")
		return nil
	}
	binPath, err := os.Executable()
	if err != nil {
		return err
	}
	binPath, _ = filepath.Abs(binPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, ".agentcookie", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	if err := keepaliveInstallAgent(launchd.Spec{
		Role:       launchd.RoleProfileKeepalive,
		BinaryPath: binPath,
		LogDir:     logDir,
		ExtraArgs:  []string{"run"},
	}); err != nil {
		return fmt.Errorf("install profile-keepalive launch agent: %w", err)
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Profile)
	}
	fmt.Fprintf(os.Stderr, "agentcookie profile-keepalive: keeping %s loaded so Claude in Chrome stays connected. While this is on, closing a profile's windows does not cut Claude in Chrome's access to it; run `agentcookie profile-keepalive disable` for that.\n", strings.Join(names, ", "))
	return nil
}

func disableProfileKeepalive() error {
	if err := keepaliveUninstallAgent(launchd.Spec{Role: launchd.RoleProfileKeepalive}); err != nil {
		return fmt.Errorf("remove profile-keepalive agent: %w", err)
	}
	if err := os.MkdirAll(keepaliveStateDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keepaliveOptedOutPath(), nil, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "agentcookie profile-keepalive: removed launch agent dev.agentcookie.profile-keepalive; it stays off until `agentcookie profile-keepalive enable`")
	removeAppIfUnused()
	return nil
}

type profileKeepaliveCheckEnv struct {
	qualify   func() []chrome.ClaudeProfile
	installed func() bool
	optedOut  func() bool
	chromePID func() (int, bool)
	loaded    func(pid int, profile string) bool
}

func checkProfileKeepalive() Check {
	root := chromepaths.ChromeProfileRoot()
	return checkProfileKeepaliveWith(profileKeepaliveCheckEnv{
		qualify: func() []chrome.ClaudeProfile {
			p, _ := chrome.QualifyingClaudeProfiles(root)
			return p
		},
		installed: func() bool { return launchd.IsInstalled(launchd.Spec{Role: launchd.RoleProfileKeepalive}) },
		optedOut:  keepaliveOptedOut,
		chromePID: func() (int, bool) { return chromectl.RunningChromePID(root) },
		loaded:    func(pid int, p string) bool { return chromectl.ProfileLoaded(pid, root, p) },
	})
}

func checkProfileKeepaliveWith(e profileKeepaliveCheckEnv) Check {
	const name = "Profile keep-alive"
	if e.optedOut() {
		return Check{Name: name, Severity: SeveritySkipped, Detail: "turned off with `profile-keepalive disable`"}
	}
	profiles := e.qualify()
	if len(profiles) == 0 {
		return Check{Name: name, Severity: SeveritySkipped, Detail: "not needed (fewer than two Chrome profiles signed into different Claude accounts)"}
	}
	if !e.installed() {
		return Check{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      "Chrome profiles are signed into different Claude accounts but the keep-alive is not running, so Claude in Chrome drops when a profile's window closes",
			Remediation: "run `agentcookie profile-keepalive enable`",
		}
	}
	pid, ok := e.chromePID()
	if !ok {
		return Check{Name: name, Severity: SeveritySkipped, Detail: "Chrome is not running"}
	}
	var missing []string
	for _, p := range profiles {
		if !e.loaded(pid, p.Profile) {
			missing = append(missing, p.Profile)
		}
	}
	if len(missing) > 0 {
		return Check{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      "not loaded: " + strings.Join(missing, ", "),
			Remediation: "check ~/.agentcookie/logs/profile-keepalive.err.log; it retries every minute",
		}
	}
	return Check{Name: name, Severity: SeverityOK, Detail: "all profiles loaded"}
}
