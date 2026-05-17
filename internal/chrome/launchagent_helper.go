package chrome

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LaunchAgentResult is the captured outcome of a one-shot LaunchAgent run.
// stdout/stderr come from the plist's StandardOutPath / StandardErrorPath.
// exit is the agent's last exit code (negative when launchctl could not
// determine it, e.g., the agent never ran).
type LaunchAgentResult struct {
	Stdout string
	Stderr string
	Exit   int
}

// RunOneShotLaunchAgent writes a temporary plist that runs argv as a
// one-shot LaunchAgent in the user's GUI session, waits for it to exit
// (capped by timeout), captures stdout/stderr/exit-code, then removes
// the plist.
//
// The point of this helper is to do Keychain mutations from a context
// where the login keychain is auto-unlocked. SSH sessions have it
// locked; the user's GUI session (where LaunchAgents run) does not.
// Operations like security set-generic-password-partition-list and
// security add-generic-password -A succeed silently from here, where
// they fail from SSH.
//
// argv[0] must be an absolute path. argv[1:] are passed as-is.
// Returns an error only for orchestration failures (write plist,
// bootstrap, bootout). A non-zero agent exit code is NOT an
// orchestration failure -- it lands in result.Exit.
func RunOneShotLaunchAgent(argv []string, timeout time.Duration) (LaunchAgentResult, error) {
	if len(argv) == 0 {
		return LaunchAgentResult{}, fmt.Errorf("RunOneShotLaunchAgent: empty argv")
	}

	label, err := randomLabel("dev.agentcookie.oneshot")
	if err != nil {
		return LaunchAgentResult{}, fmt.Errorf("generate label: %w", err)
	}

	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	stdoutPath := filepath.Join(os.TempDir(), label+".stdout")
	stderrPath := filepath.Join(os.TempDir(), label+".stderr")

	defer func() {
		// Cleanup, best-effort. The bootout above SHOULD have removed the
		// agent already; this just makes sure we don't leak a plist file
		// or an active label on the off-chance bootout failed.
		_ = exec.Command("/bin/launchctl", "bootout", launchctlTarget(label)).Run()
		_ = os.Remove(plistPath)
		_ = os.Remove(stdoutPath)
		_ = os.Remove(stderrPath)
	}()

	plistContent := renderOneShotPlist(label, argv, stdoutPath, stderrPath)
	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return LaunchAgentResult{}, fmt.Errorf("write plist: %w", err)
	}

	if out, err := exec.Command("/bin/launchctl", "bootstrap", launchctlDomain(), plistPath).CombinedOutput(); err != nil {
		return LaunchAgentResult{}, fmt.Errorf("launchctl bootstrap: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	// Poll for completion. launchctl print's "state = not running" + a
	// numeric exit code means the one-shot finished. We cap by timeout
	// to surface hangs (e.g., SecurityAgent prompt waiting on a click).
	deadline := time.Now().Add(timeout)
	pollInterval := 200 * time.Millisecond
	for time.Now().Before(deadline) {
		state, exit, ok := agentStateAndExit(label)
		if ok && state == "not running" {
			return LaunchAgentResult{
				Stdout: readFileTrim(stdoutPath),
				Stderr: readFileTrim(stderrPath),
				Exit:   exit,
			}, nil
		}
		time.Sleep(pollInterval)
	}

	// Timeout reached. Return what we have plus a synthetic "still running"
	// exit code so callers can distinguish "agent timed out" from "agent
	// exited with code 0 silently". The deferred bootout will kill it.
	return LaunchAgentResult{
		Stdout: readFileTrim(stdoutPath),
		Stderr: readFileTrim(stderrPath),
		Exit:   -1,
	}, fmt.Errorf("RunOneShotLaunchAgent: agent %q did not exit within %s (likely hung on a Keychain prompt)", label, timeout)
}

func renderOneShotPlist(label string, argv []string, stdoutPath, stderrPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>`)
	b.WriteString(plistEscape(label))
	b.WriteString(`</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, a := range argv {
		b.WriteString("    <string>")
		b.WriteString(plistEscape(a))
		b.WriteString("</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>`)
	b.WriteString(plistEscape(stdoutPath))
	b.WriteString(`</string>
  <key>StandardErrorPath</key><string>`)
	b.WriteString(plistEscape(stderrPath))
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}

// plistEscape escapes the four XML entities. plist values are XML, so
// the same rules apply. Done in code to avoid pulling in a dep.
func plistEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

func randomLabel(prefix string) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "." + hex.EncodeToString(buf), nil
}

func launchctlDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchctlTarget(label string) string {
	return launchctlDomain() + "/" + label
}

// agentStateAndExit parses `launchctl print <target>` for state and last
// exit code. Returns (state, exit, true) on success; (_, _, false) if
// the agent label is not loaded.
func agentStateAndExit(label string) (string, int, bool) {
	out, err := exec.Command("/bin/launchctl", "print", launchctlTarget(label)).CombinedOutput()
	if err != nil {
		// launchctl print returns non-zero when the label isn't loaded.
		return "", 0, false
	}
	text := string(out)
	state := scanField(text, "state =")
	exitStr := scanField(text, "last exit code =")
	if state == "" {
		return "", 0, false
	}
	if exitStr == "" || exitStr == "(never exited)" {
		return state, 0, true
	}
	exit, err := strconv.Atoi(exitStr)
	if err != nil {
		return state, 0, true
	}
	return state, exit, true
}

func scanField(text, key string) string {
	idx := strings.Index(text, key)
	if idx < 0 {
		return ""
	}
	tail := text[idx+len(key):]
	end := strings.IndexByte(tail, '\n')
	if end < 0 {
		end = len(tail)
	}
	return strings.TrimSpace(tail[:end])
}

func readFileTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}
