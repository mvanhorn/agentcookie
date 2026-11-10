package e2e

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Only this test binary and synthetic temporary files are used, never browsers.
func TestGeckoCleanupSubprocess(t *testing.T) {
	mode := os.Getenv("AGENTCOOKIE_CLEANUP_HELPER")
	if mode == "" {
		return
	}
	marker := os.Getenv("AGENTCOOKIE_CLEANUP_MARKER")
	if mode == "leader" {
		child := exec.Command(os.Args[0], "-test.run=^TestGeckoCleanupSubprocess$")
		child.Env = append(os.Environ(), "AGENTCOOKIE_CLEANUP_HELPER=child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		// Leave the child in our inherited process group and exit immediately.
		_ = child.Process.Release()
		os.Exit(0)
	}
	signal.Ignore(syscall.SIGTERM)
	for {
		if err := os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil {
			os.Exit(4)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func cleanupHelper(t *testing.T, mode, marker string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGeckoCleanupSubprocess$")
	cmd.Env = append(os.Environ(), "AGENTCOOKIE_CLEANUP_HELPER="+mode, "AGENTCOOKIE_CLEANUP_MARKER="+marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}
func waitCleanupMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(path); len(b) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("synthetic child did not start")
}
func TestGeckoCleanupKillsOwnedGroupAfterLeaderExit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "owned")
	cmd := cleanupHelper(t, "leader", marker)
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = stopOwnedFixtureGroup(cmd, 10*time.Millisecond)
		}
	})
	waitCleanupMarker(t, marker)
	foreignMarker := filepath.Join(dir, "foreign")
	foreign := cleanupHelper(t, "child", foreignMarker)
	t.Cleanup(func() { _ = stopOwnedFixtureGroup(foreign, 10*time.Millisecond) })
	waitCleanupMarker(t, foreignMarker)
	time.Sleep(50 * time.Millisecond) // the owned leader has exited; it stays unreaped
	if err := stopOwnedFixtureGroup(cmd, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("owned parent was not reaped")
	}
	if err := stopOwnedFixtureGroup(cmd, 0); err == nil {
		t.Fatal("reaped process identity must never be signaled again")
	}
	time.Sleep(50 * time.Millisecond)
	stopped, _ := os.ReadFile(marker)
	before, _ := os.ReadFile(foreignMarker)
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	live, _ := os.ReadFile(foreignMarker)
	if string(stopped) != string(after) {
		t.Fatal("owned descendant survived cleanup")
	}
	if string(before) == string(live) {
		t.Fatal("foreign process group was affected")
	}
}
func TestGeckoCleanupWaitsForBrokerLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-broker.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err = waitFixtureBrokerStopped(ctx, path)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("held broker lock accepted: %v", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = waitFixtureBrokerStopped(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err = waitFixtureBrokerStopped(ctx, filepath.Join(t.TempDir(), "not-started.lock")); err != nil {
		t.Fatal(err)
	}
}
