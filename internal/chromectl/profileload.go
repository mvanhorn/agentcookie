package chromectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChromeBinary is the executable a running Google Chrome was started from.
// Running it again hands its arguments to the running browser.
const ChromeBinary = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// ErrSenderTimeout means the process that hands a profile to the running
// Chrome did not exit in time and was killed. Chrome kills a browser that
// does not answer such a handoff within about 20 seconds, so the sender is
// killed first; Chrome itself is never signalled.
var ErrSenderTimeout = errors.New("chromectl: profile handoff did not finish in time; the sender was stopped and Chrome was left alone")

var (
	readSingletonLock = func(root string) (string, error) {
		return os.Readlink(filepath.Join(root, "SingletonLock"))
	}
	localHostname = os.Hostname
	// bonjourHostname is the machine's Bonjour name, which does not change
	// when the network hands out a different domain.
	// It is read once per process: the keep-alive checks Chrome every tick.
	bonjourHostname = sync.OnceValues(func() (string, error) {
		out, err := probe("/usr/sbin/scutil", "--get", "LocalHostName")
		return strings.TrimSpace(out), err
	})
	psStatComm = func(pid int) (string, error) {
		out, err := probe("/bin/ps", "-o", "stat=,comm=", "-p", strconv.Itoa(pid))
		return strings.TrimSpace(out), err
	}
	// lsofNames returns lsof -Fn output (one "n<path>" line per open file).
	lsofNames = func(pid int) (string, error) {
		return probe("/usr/sbin/lsof", "-n", "-P", "-Fn", "-p", strconv.Itoa(pid))
	}
	senderCommand = func(ctx context.Context, bin string, args []string) *exec.Cmd {
		return exec.CommandContext(ctx, bin, args...)
	}
)

// probeTimeout bounds ps and lsof, so a probe stuck on a process in a bad
// state reads as a failed probe instead of stalling the caller.
const probeTimeout = 5 * time.Second

func probe(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	return string(out), err
}

// RunningChromePID returns the PID of the Google Chrome that owns root, and
// whether it is safe to hand a profile to. See ChromeStatus.
func RunningChromePID(root string) (int, bool) {
	pid, ok, _ := ChromeStatus(root)
	return pid, ok
}

// ChromeStatus is RunningChromePID plus a short reason when Chrome does not
// count as running. The PID comes from Chrome's SingletonLock
// ("<host>-<pid>") and is trusted only when the lock belongs to this machine,
// the PID still runs the Chrome binary (a lock left by a crash can point at a
// PID since reused by another process), and the process is not stopped or
// stuck in the kernel.
//
// The lock keeps the hostname Chrome saw at launch, while os.Hostname follows
// the current network, so the two can drift apart (".local" at launch,
// ".localdomain" later). The lock counts as this machine's when its first DNS
// label matches the hostname or the Bonjour LocalHostName, or failing that,
// when its PID is Chrome and has files open under root.
func ChromeStatus(root string) (int, bool, string) {
	lock, err := readSingletonLock(root)
	if err != nil {
		return 0, false, "no Chrome SingletonLock"
	}
	i := strings.LastIndex(lock, "-")
	if i <= 0 {
		return 0, false, fmt.Sprintf("unreadable SingletonLock %q", lock)
	}
	pid, err := strconv.Atoi(lock[i+1:])
	if err != nil || pid <= 0 {
		return 0, false, fmt.Sprintf("unreadable SingletonLock %q", lock)
	}
	lockHost := lock[:i]
	out, err := psStatComm(pid)
	if err != nil {
		return 0, false, fmt.Sprintf("Chrome PID %d from the lock is not running", pid)
	}
	stat, comm, ok := strings.Cut(out, " ")
	if !ok || strings.TrimSpace(comm) != ChromeBinary {
		return 0, false, fmt.Sprintf("PID %d from the lock is not Google Chrome", pid)
	}
	if strings.ContainsAny(stat, "TUD") {
		return 0, false, fmt.Sprintf("Chrome PID %d is stopped or unresponsive", pid)
	}
	if host, local := lockIsLocal(lockHost, pid, root); !local {
		return 0, false, fmt.Sprintf("Chrome's lock host %q does not match this machine (%q)", lockHost, host)
	}
	return pid, true, ""
}

// lockIsLocal also returns the local hostname it compared against, for the
// caller's reason text.
func lockIsLocal(lockHost string, pid int, root string) (string, bool) {
	host, err := localHostname()
	if err == nil && sameHost(lockHost, host) {
		return host, true
	}
	if bonjour, err := bonjourHostname(); err == nil && sameHost(lockHost, bonjour) {
		return host, true
	}
	return host, holdsFilesUnder(pid, root)
}

// sameHost compares the first DNS label of two hostnames, ignoring case, so
// "MacBook-Pro-10.local" and "MacBook-Pro-10.localdomain" match.
func sameHost(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		label, _, _ := strings.Cut(s, ".")
		return label
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// holdsFilesUnder reports whether pid has any file open inside root.
func holdsFilesUnder(pid int, root string) bool {
	out, err := lsofNames(pid)
	if err != nil {
		return false
	}
	prefix := "n" + filepath.Clean(root) + string(filepath.Separator)
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// ProfileLoaded reports whether Chrome pid has files open inside the given
// profile directory, which is what "the profile is loaded" looks like from
// outside Chrome.
func ProfileLoaded(pid int, root, profile string) bool {
	return holdsFilesUnder(pid, filepath.Join(root, profile))
}

// LoadProfileWindowless asks the running Chrome to load profile without
// opening a window, so its extensions start and nothing takes focus. The
// sender normally exits within a few seconds; if it is still running at
// deadline it is killed and ErrSenderTimeout returned. That also covers a
// sender that turned into a browser of its own because Chrome had quit.
func LoadProfileWindowless(ctx context.Context, profile string, deadline time.Duration) error {
	cmd := senderCommand(ctx, ChromeBinary, []string{
		"--profile-directory=" + profile,
		"--no-startup-window",
	})
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chromectl: start profile handoff: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("chromectl: profile handoff failed: %w", err)
		}
		return nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		if err := <-done; err == nil {
			return nil
		}
		return ErrSenderTimeout
	}
}
