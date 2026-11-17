package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/launchd"
)

// fakeKeepalive is a scripted Chrome for the keep-alive loop: which profiles
// qualify, whether Chrome runs, which profiles are loaded, and what each
// load does.
type fakeKeepalive struct {
	now        time.Time
	qualify    []chrome.ClaudeProfile
	qualCalls  int
	running    bool
	reason     string
	logs       []string
	loaded     map[string]bool
	loadErr    error
	loadsTo    map[string]bool // a load of this profile marks it loaded
	loads      []string
	notes      []string
	paused     map[string]time.Time
	qualErrs   []error
	binChanged bool
	mismatches []string
	mmCalls    int
}

func newFakeKeepalive() *fakeKeepalive {
	return &fakeKeepalive{
		now: time.Date(2026, 11, 9, 22, 0, 0, 0, time.UTC),
		qualify: []chrome.ClaudeProfile{
			{Profile: "Default", AccountUUID: "a"},
			{Profile: "Profile 1", AccountUUID: "b"},
		},
		running: true,
		loaded:  map[string]bool{"Default": true, "Profile 1": true},
		loadsTo: map[string]bool{},
		paused:  map[string]time.Time{},
	}
}

func (f *fakeKeepalive) loop() *keepaliveLoop {
	return newKeepaliveLoop(keepaliveDeps{
		Root: "/root",
		Qualify: func(string) ([]chrome.ClaudeProfile, []error) {
			f.qualCalls++
			return f.qualify, f.qualErrs
		},
		ChromePID: func(string) (int, bool, string) {
			if !f.running {
				return 0, false, f.reason
			}
			return 764, true, ""
		},
		Loaded: func(_ int, _ string, p string) bool { return f.loaded[p] },
		Load: func(_ context.Context, p string) error {
			f.loads = append(f.loads, p)
			if f.loadErr != nil {
				return f.loadErr
			}
			if f.loadsTo[p] {
				f.loaded[p] = true
			}
			return nil
		},
		Notify:      func(title, body string) { f.notes = append(f.notes, body) },
		Now:         func() time.Time { return f.now },
		PausedUntil: func(p string) time.Time { return f.paused[p] },
		Logf: func(format string, a ...any) {
			f.logs = append(f.logs, fmt.Sprintf(format, a...))
		},
		BinaryChanged: func() bool { return f.binChanged },
		AccountMismatches: func() []string {
			f.mmCalls++
			return f.mismatches
		},
	})
}

func (f *fakeKeepalive) advance(d time.Duration) { f.now = f.now.Add(d) }

func TestKeepalive_ReloadsUnloadedProfile(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false
	f.loadsTo["Profile 1"] = true

	l.tick(context.Background())
	if strings.Join(f.loads, ",") != "Profile 1" {
		t.Fatalf("loads = %v, want one load of Profile 1", f.loads)
	}
	f.advance(15 * time.Second)
	l.tick(context.Background())
	if len(f.loads) != 1 {
		t.Fatalf("loaded profile must not be sent again, loads = %v", f.loads)
	}
}

func TestKeepalive_ChromeNotRunningNeverLoads(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.running = false
	f.loaded = map[string]bool{}
	f.loadsTo["Default"], f.loadsTo["Profile 1"] = true, true

	l.tick(context.Background())
	if len(f.loads) != 0 {
		t.Fatalf("must not load while Chrome is not running, loads = %v", f.loads)
	}
	f.running = true
	f.advance(15 * time.Second)
	l.tick(context.Background())
	if strings.Join(f.loads, ",") != "Default,Profile 1" {
		t.Fatalf("after Chrome starts both profiles load, got %v", f.loads)
	}
}

func TestKeepalive_PausedProfileSkippedUntilExpiry(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false
	f.loaded["Default"] = false
	f.loadsTo["Default"] = true
	f.paused["Profile 1"] = f.now.Add(10 * time.Minute)

	l.tick(context.Background())
	if strings.Join(f.loads, ",") != "Default" {
		t.Fatalf("paused Profile 1 must be skipped while Default loads, got %v", f.loads)
	}
	f.advance(9 * time.Minute)
	l.tick(context.Background())
	if len(f.loads) != 1 {
		t.Fatalf("still paused, got %v", f.loads)
	}
	f.advance(2 * time.Minute)
	l.tick(context.Background())
	if strings.Join(f.loads, ",") != "Default,Profile 1" {
		t.Fatalf("after the pause expires Profile 1 loads, got %v", f.loads)
	}
}

func TestKeepalive_NotifiesOnceAfterThreeFailedLoads(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false // loads never take

	for range 12 { // 12 ticks of 61s: well past four sends
		l.tick(context.Background())
		f.advance(61 * time.Second)
	}
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "Profile 1") {
		t.Fatalf("want exactly one notification naming Profile 1, got %v", f.notes)
	}

	// Recovery, then three more failures: one more notification.
	f.loaded["Profile 1"] = true
	l.tick(context.Background())
	f.advance(61 * time.Second)
	f.loaded["Profile 1"] = false
	for range 8 {
		l.tick(context.Background())
		f.advance(61 * time.Second)
	}
	if len(f.notes) != 2 {
		t.Fatalf("want a second notification after recovery and new failures, got %v", f.notes)
	}
}

func TestKeepalive_SkippedCyclesDoNotCount(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false

	tickFail := func() { l.tick(context.Background()); f.advance(61 * time.Second) }
	tickFail()        // send 1
	tickFail()        // send 1 fails; send 2
	f.running = false // skipped cycles: Chrome away
	tickFail()
	tickFail()
	f.running = true
	tickFail() // send 2 fails; send 3
	if len(f.notes) != 0 {
		t.Fatalf("two failures so far, no notification yet, got %v", f.notes)
	}
	tickFail() // send 3 fails: third failure
	if len(f.notes) != 1 {
		t.Fatalf("third failure notifies, got %v", f.notes)
	}
}

func TestKeepalive_WaitsAfterFailedSendButNotAfterSuccess(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false

	l.tick(context.Background()) // send
	f.advance(15 * time.Second)
	l.tick(context.Background()) // still unloaded: that send failed
	f.advance(15 * time.Second)
	l.tick(context.Background())
	if len(f.loads) != 1 {
		t.Fatalf("no new send within 60s of a failed one, loads = %v", f.loads)
	}
	f.advance(45 * time.Second)
	l.tick(context.Background())
	if len(f.loads) != 2 {
		t.Fatalf("after 60s a new send happens, loads = %v", f.loads)
	}

	// Now loads work; after a good load and a later unload, send at once.
	f.loadsTo["Profile 1"] = true
	f.advance(15 * time.Second)
	l.tick(context.Background())
	f.loaded["Profile 1"] = true
	f.advance(15 * time.Second)
	l.tick(context.Background())
	f.loaded["Profile 1"] = false
	before := len(f.loads)
	f.advance(15 * time.Second)
	l.tick(context.Background())
	if len(f.loads) != before+1 {
		t.Fatalf("an unload after a good load sends on the next cycle, loads = %v", f.loads)
	}
}

func TestKeepalive_SenderErrorCountsAsFailure(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false
	f.loadErr = errors.New("handoff timed out")

	for range 3 {
		l.tick(context.Background())
		f.advance(61 * time.Second)
	}
	if len(f.notes) != 1 {
		t.Fatalf("three failed sends notify once, got %v", f.notes)
	}
}

func TestKeepalive_GateFailingGoesIdle(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false
	f.qualify = nil

	l.tick(context.Background())
	if len(f.loads) != 0 {
		t.Fatalf("gate failing must not load, got %v", f.loads)
	}
}

func TestKeepalive_GateRecheckedAtMostOncePerMinute(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	for range 4 { // one minute of 15s ticks
		l.tick(context.Background())
		f.advance(15 * time.Second)
	}
	if f.qualCalls != 1 {
		t.Fatalf("gate read %d times in under a minute, want 1", f.qualCalls)
	}
	l.tick(context.Background())
	if f.qualCalls != 2 {
		t.Fatalf("gate must re-run after a minute, got %d", f.qualCalls)
	}

	// Gate flips to failing mid-run: the loop goes idle.
	f.qualify = nil
	f.loaded["Profile 1"] = false
	f.advance(61 * time.Second)
	l.tick(context.Background())
	if len(f.loads) != 0 {
		t.Fatalf("idle after the gate fails, loads = %v", f.loads)
	}
}

func TestKeepalive_NeverOpensProfileSyncLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	syncDir := filepath.Join(home, ".agentcookie", "profile-sync")
	if err := os.MkdirAll(syncDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeKeepalivePause("Profile 1", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_ = keepalivePausedUntil("Profile 1")
	entries, _ := os.ReadDir(syncDir)
	if len(entries) != 0 {
		t.Fatalf("keep-alive wrote into profile-sync's directory: %v", entries)
	}
}

func TestKeepalivePause_RoundTripAndValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	until := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	if err := writeKeepalivePause("Profile 1", until); err != nil {
		t.Fatal(err)
	}
	if got := keepalivePausedUntil("Profile 1"); !got.Equal(until) {
		t.Fatalf("paused until %v, want %v", got, until)
	}
	if got := keepalivePausedUntil("Default"); !got.IsZero() {
		t.Fatalf("unpaused profile reports %v", got)
	}
	if err := writeKeepalivePause("../etc", until); err == nil {
		t.Fatal("a non-profile name must be rejected")
	}
}

// Enable/disable and the wizard hook.

func stubKeepaliveInstall(t *testing.T, qualify []chrome.ClaudeProfile) (*[]launchd.Spec, *int) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	installs := []launchd.Spec{}
	uninstalls := 0
	origQ, origI, origU, origOS := keepaliveQualify, keepaliveInstallAgent, keepaliveUninstallAgent, keepaliveForceOS
	t.Cleanup(func() {
		keepaliveQualify, keepaliveInstallAgent, keepaliveUninstallAgent, keepaliveForceOS = origQ, origI, origU, origOS
	})
	keepaliveForceOS = true // exercise the macOS paths on Linux CI too
	keepaliveQualify = func(string) ([]chrome.ClaudeProfile, []error) { return qualify, nil }
	keepaliveInstallAgent = func(s launchd.Spec) error { installs = append(installs, s); return nil }
	keepaliveUninstallAgent = func(launchd.Spec) error { uninstalls++; return nil }
	return &installs, &uninstalls
}

func twoAccounts() []chrome.ClaudeProfile {
	return []chrome.ClaudeProfile{{Profile: "Default", AccountUUID: "a"}, {Profile: "Profile 1", AccountUUID: "b"}}
}

func TestKeepaliveEnable_GateFailsInstallsNothing(t *testing.T) {
	installs, _ := stubKeepaliveInstall(t, nil)
	if err := enableProfileKeepalive(false, true); err != nil {
		t.Fatalf("enable must exit cleanly when not needed, got %v", err)
	}
	if len(*installs) != 0 {
		t.Fatalf("no agent may be installed when the gate fails, got %v", *installs)
	}
}

func TestKeepaliveEnable_GatePassesInstallsRunAgent(t *testing.T) {
	installs, _ := stubKeepaliveInstall(t, twoAccounts())
	if err := enableProfileKeepalive(false, true); err != nil {
		t.Fatal(err)
	}
	if len(*installs) != 1 || (*installs)[0].Role != launchd.RoleProfileKeepalive || strings.Join((*installs)[0].ExtraArgs, " ") != "run" {
		t.Fatalf("want one profile-keepalive run agent, got %+v", *installs)
	}
}

func TestKeepaliveDisable_StaysOffForWizard(t *testing.T) {
	installs, uninstalls := stubKeepaliveInstall(t, twoAccounts())
	if err := disableProfileKeepalive(); err != nil {
		t.Fatal(err)
	}
	if *uninstalls != 1 {
		t.Fatalf("disable must remove the agent, uninstalls = %d", *uninstalls)
	}
	maybeAutoEnableProfileKeepalive()
	if len(*installs) != 0 {
		t.Fatalf("wizard must not re-enable after disable, got %v", *installs)
	}
	if err := enableProfileKeepalive(false, true); err != nil {
		t.Fatal(err)
	}
	if len(*installs) != 1 {
		t.Fatalf("explicit enable after disable installs, got %v", *installs)
	}
	maybeAutoEnableProfileKeepalive()
	if len(*installs) != 2 {
		t.Fatalf("the opt-out marker is cleared by enable, got %v", *installs)
	}
}

func TestKeepaliveWizard_OptOutFlag(t *testing.T) {
	installs, _ := stubKeepaliveInstall(t, twoAccounts())
	orig := wizardNoProfileKeepalive
	t.Cleanup(func() { wizardNoProfileKeepalive = orig })
	wizardNoProfileKeepalive = true
	maybeAutoEnableProfileKeepalive()
	if len(*installs) != 0 {
		t.Fatalf("--no-profile-keepalive must skip, got %v", *installs)
	}
}

// Doctor.

func TestCheckProfileKeepalive(t *testing.T) {
	base := profileKeepaliveCheckEnv{
		qualify:   func() ([]chrome.ClaudeProfile, []error) { return twoAccounts(), nil },
		installed: func() bool { return true },
		optedOut:  func() bool { return false },
		chromePID: func() (int, bool, string) { return 764, true, "" },
		loaded:    func(int, string) bool { return true },
	}
	cases := []struct {
		name string
		mod  func(*profileKeepaliveCheckEnv)
		want Severity
	}{
		{"all loaded", func(*profileKeepaliveCheckEnv) {}, SeverityOK},
		{"gate fails", func(e *profileKeepaliveCheckEnv) {
			e.qualify = func() ([]chrome.ClaudeProfile, []error) { return nil, nil }
		}, SeveritySkipped},
		{"store unreadable", func(e *profileKeepaliveCheckEnv) {
			e.qualify = func() ([]chrome.ClaudeProfile, []error) { return nil, []error{errors.New("operation not permitted")} }
		}, SeverityWarn},
		{"turned off", func(e *profileKeepaliveCheckEnv) { e.optedOut = func() bool { return true } }, SeveritySkipped},
		{"chrome closed", func(e *profileKeepaliveCheckEnv) {
			e.chromePID = func() (int, bool, string) { return 0, false, "no Chrome SingletonLock" }
		}, SeveritySkipped},
		{"agent missing", func(e *profileKeepaliveCheckEnv) { e.installed = func() bool { return false } }, SeverityWarn},
		{"profile unloaded", func(e *profileKeepaliveCheckEnv) { e.loaded = func(_ int, p string) bool { return p == "Default" } }, SeverityWarn},
	}
	for _, c := range cases {
		e := base
		c.mod(&e)
		got := checkProfileKeepaliveWith(e)
		if got.Severity != c.want {
			t.Errorf("%s: severity %v, want %v (%s)", c.name, got.Severity, c.want, got.Detail)
		}
		if got.Severity == SeverityFail {
			t.Errorf("%s: the keep-alive check must never FAIL", c.name)
		}
	}
}

func TestKeepalive_ChromeQuitMidTickStopsSending(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded = map[string]bool{}
	calls := 0
	l.d.ChromePID = func(string) (int, bool, string) {
		calls++
		return 764, calls <= 2, "" // running for the tick's check and the first send only
	}
	l.tick(context.Background())
	if strings.Join(f.loads, ",") != "Default" {
		t.Fatalf("after Chrome quits mid-tick no further send may run, loads = %v", f.loads)
	}
}

func TestKeepaliveEnable_ClearsOptOutEvenWhenNotNeeded(t *testing.T) {
	installs, _ := stubKeepaliveInstall(t, nil)
	if err := disableProfileKeepalive(); err != nil {
		t.Fatal(err)
	}
	if err := enableProfileKeepalive(false, true); err != nil {
		t.Fatal(err)
	}
	if keepaliveOptedOut() {
		t.Fatal("an explicit enable must clear the opt-out marker even when the gate fails")
	}
	if len(*installs) != 0 {
		t.Fatalf("nothing may be installed while the gate fails, got %v", *installs)
	}
}

func TestKeepaliveWizard_EnableFailureIsNonFatal(t *testing.T) {
	stubKeepaliveInstall(t, twoAccounts())
	orig := keepaliveAutoEnable
	t.Cleanup(func() { keepaliveAutoEnable = orig })
	called := false
	keepaliveAutoEnable = func(fromWizard, quiet bool) error {
		called = true
		if !fromWizard {
			t.Error("the wizard must call enable with fromWizard")
		}
		return errors.New("boom")
	}
	maybeAutoEnableProfileKeepalive() // must return, not panic or exit
	if !called {
		t.Fatal("wizard did not try to enable the keep-alive")
	}
}

func TestKeepalive_RepeatedGateErrorLoggedOnce(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	var logs []string
	l.d.Logf = func(format string, a ...any) { logs = append(logs, format) }
	l.d.Qualify = func(string) ([]chrome.ClaudeProfile, []error) {
		return nil, []error{errors.New("open Chrome: operation not permitted")}
	}
	for range 3 {
		l.tick(context.Background())
		f.advance(time.Minute)
	}
	n := 0
	for _, s := range logs {
		if strings.HasPrefix(s, "skipped a profile") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a repeated read error must be logged once, got %d times", n)
	}
}

func TestKeepalive_PauseAfterSendIsNotAFailure(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.loaded["Profile 1"] = false
	l.tick(context.Background()) // sends a load that does not take
	f.paused["Profile 1"] = f.now.Add(10 * time.Minute)
	for range 4 {
		f.advance(time.Minute)
		l.tick(context.Background())
	}
	if st := l.state["Profile 1"]; st.failures != 0 {
		t.Fatalf("a paused profile must not collect failures, got %d", st.failures)
	}
	if len(f.notes) != 0 {
		t.Fatalf("no notification while paused, got %v", f.notes)
	}
}

func TestKeepaliveEnable_UnreadableStoreIsAnError(t *testing.T) {
	installs, _ := stubKeepaliveInstall(t, nil)
	keepaliveQualify = func(string) ([]chrome.ClaudeProfile, []error) {
		return nil, []error{errors.New("Profile 1: operation not permitted")}
	}
	if err := enableProfileKeepalive(false, true); err == nil {
		t.Fatal("enable must report an unreadable store instead of saying not needed")
	}
	if len(*installs) != 0 {
		t.Fatalf("nothing may be installed, got %v", *installs)
	}
}

func detectionLogs(logs []string) []string {
	var out []string
	for _, l := range logs {
		if strings.HasPrefix(l, "Chrome ") {
			out = append(out, l)
		}
	}
	return out
}

// When the keep-alive stops seeing Chrome it says so once, with the reason,
// instead of going quiet; when Chrome comes back it says so once.
func TestKeepalive_LogsChromeDetectionTransitionsOnce(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	ctx := context.Background()

	l.tick(ctx)
	if got := detectionLogs(f.logs); len(got) != 0 {
		t.Fatalf("steady running Chrome must not log detection lines, got %v", got)
	}

	f.running = false
	f.reason = `Chrome's lock host "a.local" does not match this machine ("a.localdomain")`
	for range 3 {
		f.advance(keepaliveTick)
		l.tick(ctx)
	}
	got := detectionLogs(f.logs)
	if len(got) != 1 || !strings.Contains(got[0], "not detected") || !strings.Contains(got[0], "a.localdomain") {
		t.Fatalf("want one not-detected line naming the reason, got %v", got)
	}

	f.running = true
	f.advance(keepaliveTick)
	l.tick(ctx)
	f.advance(keepaliveTick)
	l.tick(ctx)
	got = detectionLogs(f.logs)
	if len(got) != 2 || !strings.Contains(got[1], "detected") || strings.Contains(got[1], "not detected") {
		t.Fatalf("want one detected line after recovery, got %v", got)
	}
}

func TestKeepalive_RestartsWhenBinaryChanges(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	l.tick(context.Background())
	if l.restart != nil {
		t.Fatalf("unchanged binary must not restart")
	}
	f.binChanged = true
	f.advance(time.Minute)
	l.tick(context.Background())
	if !errors.Is(l.restart, errKeepaliveRestart) {
		t.Fatalf("restart = %v, want errKeepaliveRestart after the binary changed", l.restart)
	}
	if len(f.loads) != 0 {
		t.Fatalf("a restarting loop must not load profiles, loads = %v", f.loads)
	}
}

func TestKeepalive_RunReturnsRestartError(t *testing.T) {
	f := newFakeKeepalive()
	f.binChanged = true
	err := f.loop().run(context.Background())
	if !errors.Is(err, errKeepaliveRestart) {
		t.Fatalf("run = %v, want errKeepaliveRestart so launchd restarts the job", err)
	}
}

func TestKeepalive_PermissionErrorRestartsOnlyAStaleProcess(t *testing.T) {
	f := newFakeKeepalive()
	f.qualify = nil
	f.qualErrs = []error{&fs.PathError{Op: "open", Path: "/root", Err: fs.ErrPermission}}
	l := f.loop()

	l.tick(context.Background())
	if l.restart != nil {
		t.Fatalf("a fresh process must not restart on a permission error")
	}
	f.advance(keepaliveStaleAfter)
	l.tick(context.Background())
	if !errors.Is(l.restart, errKeepaliveRestart) {
		t.Fatalf("restart = %v, want errKeepaliveRestart for a long-running process refused by macOS", l.restart)
	}
}

func TestKeepalive_OtherReadErrorsNeverRestart(t *testing.T) {
	f := newFakeKeepalive()
	f.qualify = nil
	f.qualErrs = []error{errors.New("Profile 1: corrupt store")}
	l := f.loop()
	f.advance(keepaliveStaleAfter + time.Hour)
	l.tick(context.Background())
	if l.restart != nil {
		t.Fatalf("a non-permission error must not restart, got %v", l.restart)
	}
}

func TestKeepalive_NotifiesOnceForAccountMismatch(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	f.mismatches = []string{"Default: uses the account Profile 1 uses"}

	l.tick(context.Background())
	if len(f.notes) != 1 || !strings.Contains(f.notes[0], "other Claude account") {
		t.Fatalf("notes = %v, want one mismatch notification", f.notes)
	}
	f.advance(keepaliveAccountsEvery)
	l.tick(context.Background())
	if len(f.notes) != 1 {
		t.Fatalf("the same mismatch must not notify again, notes = %v", f.notes)
	}
	f.mismatches = nil
	f.advance(keepaliveAccountsEvery)
	l.tick(context.Background())
	if !strings.Contains(strings.Join(f.logs, "\n"), "match claude.ai again") {
		t.Fatalf("clearing must be logged, logs = %v", f.logs)
	}
	f.mismatches = []string{"Default: uses the account Profile 1 uses"}
	f.advance(keepaliveAccountsEvery)
	l.tick(context.Background())
	if len(f.notes) != 2 {
		t.Fatalf("a mismatch that comes back must notify again, notes = %v", f.notes)
	}
}

func TestKeepalive_AccountCheckIsSpaced(t *testing.T) {
	f := newFakeKeepalive()
	l := f.loop()
	l.tick(context.Background())
	f.advance(keepaliveTick)
	l.tick(context.Background())
	if f.mmCalls != 1 {
		t.Fatalf("account comparison ran %d times within %s, want 1", f.mmCalls, keepaliveAccountsEvery)
	}
	f.advance(keepaliveAccountsEvery)
	l.tick(context.Background())
	if f.mmCalls != 2 {
		t.Fatalf("account comparison ran %d times, want 2 after %s", f.mmCalls, keepaliveAccountsEvery)
	}
}

func TestExecutableChangedSinceStart(t *testing.T) {
	// The test binary itself is the executable; it does not change mid-test.
	if executableChangedSinceStart()() {
		t.Fatalf("an untouched binary must not report a change")
	}
}
