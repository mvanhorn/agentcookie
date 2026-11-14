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
	psOut    map[int]string // pid -> "STAT COMM"
	lsofOut  string
	senderFn func(ctx context.Context, bin string, args []string) *exec.Cmd
}

func (f fakeHost) install(t *testing.T) {
	t.Helper()
	origLock, origHost, origPS, origLsof, origSender := readSingletonLock, localHostname, psStatComm, lsofNames, senderCommand
	t.Cleanup(func() {
		readSingletonLock, localHostname, psStatComm, lsofNames, senderCommand = origLock, origHost, origPS, origLsof, origSender
	})
	readSingletonLock = func(string) (string, error) { return f.lock, f.lockErr }
	localHostname = func() (string, error) { return f.host, nil }
	psStatComm = func(pid int) (string, error) {
		out, ok := f.psOut[pid]
		if !ok {
			return "", errors.New("no such process")
		}
		return out, nil
	}
	lsofNames = func(int) (string, error) { return f.lsofOut, nil }
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
