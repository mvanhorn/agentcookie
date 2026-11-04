//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// syscallKillSelf sends the test process SIGTERM, as launchd does on bootout.
func syscallKillSelf() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) }

func TestStartPassesExitCodeAndArgs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "args")
	code, err := start("/bin/sh", []string{"/bin/sh", "-c", `printf '%s' "$0 $1" > "$OUT"; exit 7`, "a b"}, append(os.Environ(), "OUT="+out))
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	// With sh -c, the next argument is $0, so this checks argv and env.
	if b, _ := os.ReadFile(out); string(b) != "a b " {
		t.Errorf("child wrote %q, want %q", b, "a b ")
	}
}

func TestStartForwardsSIGTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	go func() {
		for range 100 {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = syscallKillSelf()
	}()
	code, err := start("/bin/sh", []string{"/bin/sh", "-c", `touch "$READY"; exec sleep 30`}, append(os.Environ(), "READY="+ready))
	if err != nil {
		t.Fatal(err)
	}
	if code != 128+15 {
		t.Errorf("exit code = %d, want 143 (child stopped by the forwarded SIGTERM)", code)
	}
}
