package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeExec records the exec the launcher asked for instead of replacing the
// test process.
type fakeExec struct {
	path string
	argv []string
}

func (f *fakeExec) exec(path string, argv []string, _ []string) error {
	f.path, f.argv = path, argv
	return nil
}

func executable(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agentcookie")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func accept(string) error { return nil }

func TestLauncherExecsTargetWithItsArgs(t *testing.T) {
	target := executable(t)
	var fx fakeExec
	var stderr strings.Builder
	code := run([]string{"/app/agentcookie-launcher", target, "source", "--watch"}, accept, fx.exec, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("run = %d, stderr %q", code, stderr.String())
	}
	want, _ := filepath.EvalSymlinks(target)
	if fx.path != want || strings.Join(fx.argv, "|") != want+"|source|--watch" {
		t.Fatalf("exec(%q, %q), want the target with argv[1:]", fx.path, fx.argv)
	}
}

func TestLauncherWithNoArgumentsDoesNothing(t *testing.T) {
	var fx fakeExec
	var stderr strings.Builder
	if code := run([]string{"/app/agentcookie-launcher"}, accept, fx.exec, &stderr); code != 0 {
		t.Fatalf("run = %d, want 0 when opened without arguments", code)
	}
	if fx.path != "" {
		t.Fatalf("exec ran %q with no target", fx.path)
	}
}

// Refusals exit 0 so launchd's KeepAlive (relaunch on non-zero exit) leaves
// the job stopped instead of restarting it every few seconds.
func TestLauncherRefusesBadTargetsWithoutCrashLooping(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"relative":       "agentcookie",
		"missing":        filepath.Join(dir, "nope"),
		"not executable": plain,
		"directory":      dir,
	} {
		t.Run(name, func(t *testing.T) {
			var fx fakeExec
			var stderr strings.Builder
			if code := run([]string{"/app/agentcookie-launcher", target, "source"}, accept, fx.exec, &stderr); code != 0 {
				t.Fatalf("run = %d, want 0", code)
			}
			if fx.path != "" || !strings.Contains(stderr.String(), "agentcookie-launcher:") {
				t.Fatalf("exec %q, stderr %q", fx.path, stderr.String())
			}
		})
	}
}

func TestLauncherRefusesUnsignedTarget(t *testing.T) {
	target := executable(t)
	var fx fakeExec
	var stderr strings.Builder
	reject := func(string) error { return errors.New("not a release-signed agentcookie") }
	if code := run([]string{"/app/agentcookie-launcher", target, "source"}, reject, fx.exec, &stderr); code != 0 {
		t.Fatalf("run = %d, want 0", code)
	}
	if fx.path != "" {
		t.Fatalf("exec ran %q after the signature check failed", fx.path)
	}
	if !strings.Contains(stderr.String(), "release-signed") || !strings.Contains(stderr.String(), "services refresh") {
		t.Fatalf("stderr %q should name the signature problem and the fix", stderr.String())
	}
}

func TestLauncherVerifiesResolvedSymlink(t *testing.T) {
	target := executable(t)
	link := filepath.Join(t.TempDir(), "agentcookie")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(target)
	var checked string
	var fx fakeExec
	verify := func(p string) error { checked = p; return nil }
	if code := run([]string{"/app/agentcookie-launcher", link, "cmux-sync"}, verify, fx.exec, &strings.Builder{}); code != 0 {
		t.Fatalf("run = %d", code)
	}
	if checked != resolved || fx.path != resolved {
		t.Fatalf("verified %q and exec'd %q, want both %q", checked, fx.path, resolved)
	}
}
