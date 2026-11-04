// Command agentcookie-launcher is the executable inside agentcookie.app.
// agentcookie's LaunchAgents on macOS start it with the agentcookie binary and
// that binary's arguments:
//
//	agentcookie-launcher /path/to/agentcookie source --watch
//
// Because the job's program lives inside the app, macOS lists the job in
// Login Items as "agentcookie" with the app's icon instead of under the
// signing certificate's personal name.
//
// On macOS the launcher starts agentcookie as a child that disclaims
// responsibility, then waits for it and passes on its exit status and the
// signals launchd sends. Without the disclaim, macOS would hold the app
// responsible for agentcookie's privacy checks, and agentcookie would lose
// the access to Chrome's profile folder it was granted: an exec into
// agentcookie keeps the app as the responsible code, so it is not enough.
// The child is the same signed agentcookie as before, so Keychain sees the
// same code identity.
//
// The launcher runs only a release-signed agentcookie, so no other program
// can borrow the name. Opened from Finder with no arguments, it does nothing.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mvanhorn/agentcookie/internal/macapp"
)

func main() {
	os.Exit(run(os.Args, verify, start, os.Stderr))
}

// starter runs path with argv and env and returns the exit code the launcher
// should exit with: the target's own.
type starter func(path string, argv, env []string) (int, error)

// run checks the target named by args[1], runs it with args[1:] and returns
// its exit code. When it refuses or cannot start the target it returns 0:
// the LaunchAgents set KeepAlive to relaunch only on a non-zero exit, so a
// refusal leaves the job stopped instead of restarting it every few seconds.
// The next `agentcookie services refresh` rewrites the job to run agentcookie
// directly.
func run(args []string, verify func(string) error, start starter, stderr io.Writer) int {
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
	code, err := start(resolved, argv, os.Environ())
	if err != nil {
		return refuse("start %s: %v", resolved, err)
	}
	return code
}

func verify(path string) error {
	if err := macapp.VerifyTarget(path); err != nil {
		return fmt.Errorf("not a release-signed agentcookie (%v)", err)
	}
	return nil
}
