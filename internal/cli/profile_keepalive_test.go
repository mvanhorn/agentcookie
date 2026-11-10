package cli

import (
	"context"
	"errors"
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
	now       time.Time
	qualify   []chrome.ClaudeProfile
	qualCalls int
	running   bool
	loaded    map[string]bool
	loadErr   error
	loadsTo   map[string]bool // a load of this profile marks it loaded
	loads     []string
	notes     []string
	paused    map[string]time.Time
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
			return f.qualify, nil
		},
		ChromePID: func(string) (int, bool) { return 764, f.running },
		Loaded:    func(_ int, _ string, p string) bool { return f.loaded[p] },
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
		Logf:        func(string, ...any) {},
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

	for i := 0; i < 12; i++ { // 12 ticks of 61s: well past four sends
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
	for i := 0; i < 8; i++ {
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

	for i := 0; i < 3; i++ {
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
	for i := 0; i < 4; i++ { // one minute of 15s ticks
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
		qualify:   func() []chrome.ClaudeProfile { return twoAccounts() },
		installed: func() bool { return true },
		optedOut:  func() bool { return false },
		chromePID: func() (int, bool) { return 764, true },
		loaded:    func(int, string) bool { return true },
	}
	cases := []struct {
		name string
		mod  func(*profileKeepaliveCheckEnv)
		want Severity
	}{
		{"all loaded", func(*profileKeepaliveCheckEnv) {}, SeverityOK},
		{"gate fails", func(e *profileKeepaliveCheckEnv) { e.qualify = func() []chrome.ClaudeProfile { return nil } }, SeveritySkipped},
		{"turned off", func(e *profileKeepaliveCheckEnv) { e.optedOut = func() bool { return true } }, SeveritySkipped},
		{"chrome closed", func(e *profileKeepaliveCheckEnv) { e.chromePID = func() (int, bool) { return 0, false } }, SeveritySkipped},
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
