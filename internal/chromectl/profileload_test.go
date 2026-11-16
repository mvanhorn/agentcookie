package chromectl

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const testRoot = "/Users/u/Library/Application Support/Google/Chrome"

// fakeHost swaps every outside call profileload makes, so no test reads the
// developer's real Chrome lock or runs ps, lsof or Chrome.
type fakeHost struct {
	lock     string
	lockErr  error
	host     string
	bonjour  string
	psOut    map[int]string // pid -> "STAT COMM"
	lsofOut  string
	lsofErr  error
	senderFn func(ctx context.Context, bin string, args []string) *exec.Cmd
}

func (f fakeHost) install(t *testing.T) {
	t.Helper()
	origLock, origHost, origBonjour, origPS, origLsof, origSender := readSingletonLock, localHostname, bonjourHostname, psStatComm, lsofNames, senderCommand
	t.Cleanup(func() {
		readSingletonLock, localHostname, bonjourHostname, psStatComm, lsofNames, senderCommand = origLock, origHost, origBonjour, origPS, origLsof, origSender
	})
	readSingletonLock = func(string) (string, error) { return f.lock, f.lockErr }
	localHostname = func() (string, error) {
		if f.host == "" {
			return "", errors.New("no hostname")
		}
		return f.host, nil
	}
	bonjourHostname = func() (string, error) {
		if f.bonjour == "" {
			return "", errors.New("no LocalHostName")
		}
		return f.bonjour, nil
	}
	psStatComm = func(pid int) (string, error) {
		out, ok := f.psOut[pid]
		if !ok {
			return "", errors.New("no such process")
		}
		return out, nil
	}
	lsofNames = func(int) (string, error) { return f.lsofOut, f.lsofErr }
	if f.senderFn != nil {
		senderCommand = f.senderFn
	}
}

func liveChrome() fakeHost {
	return fakeHost{
		lock:  "MacBook-Pro-10.local-764",
		host:  "MacBook-Pro-10",
		psOut: map[int]string{764: "S " + ChromeBinary},
	}
}

func TestRunningChromePID_Verified(t *testing.T) {
	liveChrome().install(t)
	pid, ok := RunningChromePID(testRoot)
	if !ok || pid != 764 {
		t.Fatalf("got pid=%d ok=%v, want 764 true", pid, ok)
	}
}

func TestRunningChromePID_NoLock(t *testing.T) {
	f := liveChrome()
	f.lockErr = errors.New("missing")
	f.install(t)
	if _, ok := RunningChromePID(testRoot); ok {
		t.Fatal("missing SingletonLock must report not running")
	}
}

func TestRunningChromePID_DeadPID(t *testing.T) {
	f := liveChrome()
	f.psOut = map[int]string{}
	f.install(t)
	if _, ok := RunningChromePID(testRoot); ok {
		t.Fatal("dead PID must report not running")
	}
}

func TestRunningChromePID_ReusedPIDIsNotChrome(t *testing.T) {
	f := liveChrome()
	f.psOut = map[int]string{764: "S /usr/sbin/cfprefsd"}
	f.install(t)
	if _, ok := RunningChromePID(testRoot); ok {
		t.Fatal("a live non-Chrome PID from a stale lock must report not running")
	}
}

func TestRunningChromePID_OtherMachineLock(t *testing.T) {
	f := liveChrome()
	f.lock = "other-mac.local-764"
	f.install(t)
	if _, ok := RunningChromePID(testRoot); ok {
		t.Fatal("a lock from another hostname must report not running")
	}
}

// The lock keeps the hostname Chrome saw at launch; os.Hostname follows the
// current network. A suffix change after launch must not hide Chrome.
func TestRunningChromePID_HostSuffixDrift(t *testing.T) {
	for _, tc := range []struct{ lock, host string }{
		{"MacBook-Pro-10.local-764", "MacBook-Pro-10.localdomain"},
		{"MacBook-Pro-10.local-764", "macbook-pro-10"},
		{"macbook-pro-10-764", "MacBook-Pro-10.lan"},
		{"MacBook-Pro-10.home-764", "MacBook-Pro-10.local"},
	} {
		f := liveChrome()
		f.lock, f.host = tc.lock, tc.host
		f.install(t)
		if pid, ok := RunningChromePID(testRoot); !ok || pid != 764 {
			t.Errorf("lock %q host %q: got pid=%d ok=%v, want 764 true", tc.lock, tc.host, pid, ok)
		}
	}
}

func TestRunningChromePID_BonjourNameMatchesWhenHostnameFails(t *testing.T) {
	f := liveChrome()
	f.host = ""
	f.bonjour = "MacBook-Pro-10"
	f.install(t)
	if pid, ok := RunningChromePID(testRoot); !ok || pid != 764 {
		t.Fatalf("got pid=%d ok=%v, want 764 true via LocalHostName", pid, ok)
	}
}

// A launch under an unrelated name (a DHCP-assigned hostname, say) is still
// this machine's Chrome when the PID is Chrome and holds files in root.
func TestRunningChromePID_UnrelatedLockNameButPIDHoldsRoot(t *testing.T) {
	f := liveChrome()
	f.lock = "dhcp-10-0-0-5.corp-764"
	f.host = "MacBook-Pro-10.localdomain"
	f.lsofOut = "n" + testRoot + "/Default/Cookies"
	f.install(t)
	if pid, ok := RunningChromePID(testRoot); !ok || pid != 764 {
		t.Fatalf("got pid=%d ok=%v, want 764 true via open files", pid, ok)
	}
}

func TestChromeStatus_ReasonNamesBothHosts(t *testing.T) {
	f := liveChrome()
	f.lock = "other-mac.local-764"
	f.host = "MacBook-Pro-10.localdomain"
	f.install(t)
	_, ok, reason := ChromeStatus(testRoot)
	if ok {
		t.Fatal("want not running")
	}
	if !strings.Contains(reason, "other-mac.local") || !strings.Contains(reason, "MacBook-Pro-10.localdomain") {
		t.Fatalf("reason %q must name the lock host and the local host", reason)
	}
}

func TestChromeStatus_ReasonNamesLsofFailure(t *testing.T) {
	f := liveChrome()
	f.lock = "other-mac.local-764"
	f.host = "MacBook-Pro-10.localdomain"
	f.lsofErr = errors.New("lsof timed out")
	f.install(t)
	_, ok, reason := ChromeStatus(testRoot)
	if ok {
		t.Fatal("want not running")
	}
	if !strings.Contains(reason, "lsof timed out") {
		t.Fatalf("reason %q must name the lsof failure, not only the host mismatch", reason)
	}
}

func TestRunningChromePID_StoppedProcessFailsGuard(t *testing.T) {
	for _, stat := range []string{"T", "U", "D", "Ts"} {
		f := liveChrome()
		f.psOut = map[int]string{764: stat + " " + ChromeBinary}
		f.install(t)
		if _, ok := RunningChromePID(testRoot); ok {
			t.Fatalf("stat %q must fail the responsiveness guard", stat)
		}
	}
}

func TestProfileLoaded(t *testing.T) {
	f := liveChrome()
	f.lsofOut = strings.Join([]string{
		"n" + testRoot + "/Default/Cookies",
		"n" + testRoot + "/Default/History",
		"n" + testRoot + "/Profile 10/Cookies",
		"n/usr/lib/libSystem.B.dylib",
	}, "\n")
	f.install(t)

	if !ProfileLoaded(764, testRoot, "Default") {
		t.Error("Default has open files, want loaded")
	}
	if ProfileLoaded(764, testRoot, "Profile 1") {
		t.Error("Profile 1 has no open files (Profile 10 must not count), want unloaded")
	}
}

func TestLoadProfileWindowless_ArgsAndQuickExit(t *testing.T) {
	var gotBin string
	var gotArgs []string
	f := liveChrome()
	f.senderFn = func(ctx context.Context, bin string, args []string) *exec.Cmd {
		gotBin, gotArgs = bin, args
		return exec.CommandContext(ctx, "/usr/bin/true")
	}
	f.install(t)

	if err := LoadProfileWindowless(context.Background(), "Profile 1", time.Second); err != nil {
		t.Fatalf("quick exit must succeed, got %v", err)
	}
	if gotBin != ChromeBinary {
		t.Errorf("bin = %q, want the Chrome binary, never open(1)", gotBin)
	}
	want := []string{"--profile-directory=Profile 1", "--no-startup-window"}
	if strings.Join(gotArgs, "|") != strings.Join(want, "|") {
		t.Errorf("args = %q, want %q", gotArgs, want)
	}
}

func TestLoadProfileWindowless_StuckSenderIsKilledAtDeadline(t *testing.T) {
	var sender *exec.Cmd
	f := liveChrome()
	f.senderFn = func(ctx context.Context, bin string, args []string) *exec.Cmd {
		sender = exec.Command("/bin/sleep", "30")
		return sender
	}
	f.install(t)

	start := time.Now()
	err := LoadProfileWindowless(context.Background(), "Profile 1", 200*time.Millisecond)
	if !errors.Is(err, ErrSenderTimeout) {
		t.Fatalf("want ErrSenderTimeout, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("deadline not enforced")
	}
	if sender.ProcessState == nil || sender.ProcessState.Success() {
		t.Fatal("the stuck sender must have been killed and reaped")
	}
}
