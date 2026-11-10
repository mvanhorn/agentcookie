package profilelock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestLockLifecycle(t *testing.T) {
	p := t.TempDir()
	l, e := Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Acquire(p); !errors.Is(e, ErrBrowserRunning) {
		t.Fatalf("second lock: %v", e)
	}
	l.Close()
	l, e = Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	l.Close()
	if e = os.WriteFile(filepath.Join(p, "WebDriverBiDiServer.json"), []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Acquire(p); !errors.Is(e, ErrBrowserRunning) {
		t.Fatal(e)
	}
}
func TestLinuxSymlinkLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux legacy symlink")
	}
	p := t.TempDir()
	if e := os.Symlink("127.0.0.1:+"+strconv.Itoa(os.Getpid()), filepath.Join(p, "lock")); e != nil {
		t.Fatal(e)
	}
	if _, e := Acquire(p); !errors.Is(e, ErrBrowserRunning) {
		t.Fatal(e)
	}
}
func TestProfileLockHelper(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LOCK_HELPER") == "" {
		return
	}
	l, e := Acquire(os.Getenv("AGENTCOOKIE_LOCK_PROFILE"))
	if e != nil {
		os.Exit(2)
	}
	defer l.Close()
	fmt.Println("locked")
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
}
func TestOtherProcessLockRefusal(t *testing.T) {
	p := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProfileLockHelper$")
	cmd.Env = append(os.Environ(), "AGENTCOOKIE_LOCK_HELPER=1", "AGENTCOOKIE_LOCK_PROFILE="+p)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { stdin.Close(); cmd.Wait() }()
	line, e := bufio.NewReader(stdout).ReadString('\n')
	if e != nil || line != "locked\n" {
		t.Fatal(line, e)
	}
	if _, e = Acquire(p); !errors.Is(e, ErrBrowserRunning) {
		t.Fatal(e)
	}
}
