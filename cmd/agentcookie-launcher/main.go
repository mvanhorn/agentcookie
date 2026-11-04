// Command agentcookie-launcher is the executable inside agentcookie.app.
// agentcookie's LaunchAgents on macOS start it with the agentcookie binary and
// that binary's arguments:
//
//	agentcookie-launcher /path/to/agentcookie source --watch
//
// Because the job's program lives inside the app, macOS lists the job in
// Login Items as "agentcookie" with the app's icon instead of under the
// signing certificate's personal name. The launcher then replaces itself with
// agentcookie (exec, same pid), so the daemon runs exactly as before, with
// the same code identity for Keychain access.
//
// The launcher runs only a release-signed agentcookie, so no other program
// can borrow the name. Opened from Finder with no arguments, it does nothing.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mvanhorn/agentcookie/internal/macapp"
)

func main() {
	os.Exit(run(os.Args, verify, syscall.Exec, os.Stderr))
}

// run checks the target named by args[1] and execs it with args[1:]. It
// returns only when it does not exec, and then always 0: the LaunchAgents set
// KeepAlive to relaunch only on a non-zero exit, so a refusal leaves the job
// stopped instead of restarting it every few seconds. The next
// `agentcookie services refresh` rewrites the job to run agentcookie directly.
func run(args []string, verify func(string) error, execve func(string, []string, []string) error, stderr io.Writer) int {
	if len(args) < 2 {
		return 0
	}
	refuse := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "agentcookie-launcher: "+format+"; run `agentcookie services refresh`\n", a...)
		return 0
	}
	target := args[1]
	if !filepath.IsAbs(target) {
		return refuse("%s: the agentcookie binary must be an absolute path", target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return refuse("%v", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return refuse("%v", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return refuse("%s is not an executable file", resolved)
	}
	if err := verify(resolved); err != nil {
		return refuse("refusing to run %s: %v", resolved, err)
	}
	argv := append([]string{resolved}, args[2:]...)
	if err := execve(resolved, argv, os.Environ()); err != nil {
		return refuse("exec %s: %v", resolved, err)
	}
	return 0
}

func verify(path string) error {
	if err := macapp.VerifyTarget(path); err != nil {
		return fmt.Errorf("not a release-signed agentcookie (%v)", err)
	}
	return nil
}
