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
	psStatComm    = func(pid int) (string, error) {
		out, err := exec.Command("/bin/ps", "-o", "stat=,comm=", "-p", strconv.Itoa(pid)).Output()
		return strings.TrimSpace(string(out)), err
	}
	// lsofNames returns lsof -Fn output (one "n<path>" line per open file).
	lsofNames = func(pid int) (string, error) {
		out, err := exec.Command("/usr/sbin/lsof", "-n", "-P", "-Fn", "-p", strconv.Itoa(pid)).Output()
		return string(out), err
	}
	senderCommand = func(ctx context.Context, bin string, args []string) *exec.Cmd {
		return exec.CommandContext(ctx, bin, args...)
	}
)

// RunningChromePID returns the PID of the Google Chrome that owns root, and
// whether it is safe to hand a profile to. The PID comes from Chrome's
// SingletonLock ("<host>-<pid>") and is trusted only when the lock was
// written on this machine, the PID still runs the Chrome binary (a lock left
// by a crash can point at a PID since reused by another process), and the
// process is not stopped or stuck in the kernel.
func RunningChromePID(root string) (int, bool) {
	lock, err := readSingletonLock(root)
	if err != nil {
		return 0, false
	}
	i := strings.LastIndex(lock, "-")
	if i <= 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(lock[i+1:])
	if err != nil || pid <= 0 {
		return 0, false
	}
	host, err := localHostname()
	if err != nil || !sameHost(lock[:i], host) {
		return 0, false
	}
	out, err := psStatComm(pid)
	if err != nil {
		return 0, false
	}
	stat, comm, ok := strings.Cut(out, " ")
	if !ok || strings.TrimSpace(comm) != ChromeBinary {
		return 0, false
	}
	if strings.ContainsAny(stat, "TUD") {
		return 0, false
	}
	return pid, true
}

// sameHost compares hostnames ignoring case and a trailing ".local", since
// Chrome writes the Bonjour name and os.Hostname may not.
func sameHost(a, b string) bool {
	norm := func(s string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".local")
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// ProfileLoaded reports whether Chrome pid has files open inside the given
// profile directory, which is what "the profile is loaded" looks like from
// outside Chrome.
func ProfileLoaded(pid int, root, profile string) bool {
	out, err := lsofNames(pid)
	if err != nil {
		return false
	}
	prefix := "n" + filepath.Join(root, profile) + string(filepath.Separator)
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
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
