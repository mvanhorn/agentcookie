// Package agentbrowser wires Chromium agent browsers (browser-use,
// vercel-labs agent-browser) onto a CDP endpoint so they attach to the
// user's real Chrome session instead of launching an empty profile. It
// is the wiring half of the attach broker; internal/agentattach finds
// the endpoint, this package points the agent browsers at it.
//
// Each agent browser is a Wirer. Wiring is durable: because these tools
// take the CDP endpoint as a launch flag rather than a persisted config
// key, Wire writes a small launcher script under ~/.agentcookie/
// agent-browser/ that execs the real binary with the attach flag. The
// agent (or the PP HAR-sniff flow) invokes the launcher and is always
// attached; nothing in the agent browser's own config is mutated.
package agentbrowser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AttachTarget identifies the running Chrome to attach to. Different
// agent browsers want different shapes of the same endpoint: browser-use
// takes a ws:// or http:// URL (--cdp-url), agent-browser takes the bare
// debug port (--cdp). Both are carried so each adapter uses what it needs.
type AttachTarget struct {
	// Port is the loopback Chrome remote-debugging port (e.g. 9222).
	Port int
	// WSEndpoint is the browser-level webSocketDebuggerUrl when known
	// (from agentattach discovery); may be empty if only the port is known.
	WSEndpoint string
}

// Wirer is one Chromium agent browser the broker can attach to a CDP
// endpoint.
type Wirer interface {
	// Name is the stable identifier used by `attach --target`.
	Name() string
	// IsInstalled reports whether the agent browser binary is present.
	IsInstalled() bool
	// LauncherPath returns the path Wire writes its launcher to, whether
	// or not it exists yet.
	LauncherPath() string
	// Wire makes future invocations attach to target and returns what it
	// did (the launcher path). It is idempotent: re-wiring the same target
	// rewrites identical bytes.
	Wire(target AttachTarget) (WireResult, error)
	// LaunchSnippet returns a copy-pasteable command that attaches to
	// target without writing anything.
	LaunchSnippet(target AttachTarget) string
}

// IsWired reports whether w's launcher has been written (attach --wire run).
func IsWired(w Wirer) bool {
	info, err := os.Stat(w.LauncherPath())
	return err == nil && !info.IsDir()
}

// WireResult describes a completed Wire.
type WireResult struct {
	// LauncherPath is the executable the user/agent should invoke to get
	// an attached session.
	LauncherPath string
}

// launcherSubdir is the subdirectory under ~/.agentcookie where launcher
// scripts are written.
const launcherSubdir = "agent-browser"

// binaryInstalled reports whether path exists and is a regular file.
func binaryInstalled(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirOverride lets tests redirect the launcher directory. Empty means
// the real ~/.agentcookie/agent-browser.
var dirOverride string

// baseDir returns the launcher directory without creating it.
func baseDir() (string, error) {
	if dirOverride != "" {
		return dirOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentcookie", launcherSubdir), nil
}

// launcherDir returns the directory launcher scripts are written to,
// creating it if needed.
func launcherDir() (string, error) {
	base, err := baseDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return base, nil
}

// launcherPathNoCreate computes the launcher path for filename without
// creating the directory. Used by LauncherPath()/IsWired().
func launcherPathNoCreate(filename string) string {
	base, err := baseDir()
	if err != nil {
		return filename
	}
	return filepath.Join(base, filename)
}

// validateEndpoint rejects anything that is not a plain ws://, wss://, or
// http(s):// URL, so a hostile or malformed endpoint can never be
// embedded into the generated launcher script as shell metacharacters.
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("agentbrowser: empty CDP endpoint")
	}
	ok := strings.HasPrefix(endpoint, "ws://") ||
		strings.HasPrefix(endpoint, "wss://") ||
		strings.HasPrefix(endpoint, "http://") ||
		strings.HasPrefix(endpoint, "https://")
	if !ok {
		return fmt.Errorf("agentbrowser: CDP endpoint %q is not a ws:// or http:// URL", endpoint)
	}
	if strings.ContainsAny(endpoint, " \t\n\r\"'`$\\;&|<>(){}") {
		return fmt.Errorf("agentbrowser: CDP endpoint %q contains illegal characters", endpoint)
	}
	return nil
}

// writeLauncher writes an executable shell wrapper at dir/filename that
// execs realBinary with the given attach args, then returns its path.
// The endpoint must already be validated by the caller. Bytes are stable
// for a given (realBinary, args) so re-wiring is idempotent.
func writeLauncher(dir, filename, realBinary string, attachArgs []string) (string, error) {
	path := filepath.Join(dir, filename)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by `agentcookie attach`. Runs the agent browser attached to\n")
	b.WriteString("# your real Chrome over CDP. Re-run `agentcookie attach` to refresh.\n")
	b.WriteString("exec ")
	b.WriteString(shellQuote(realBinary))
	for _, a := range attachArgs {
		b.WriteString(" ")
		b.WriteString(shellQuote(a))
	}
	b.WriteString(" \"$@\"\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		return "", err
	}
	// WriteFile honors the mode only on create; ensure executable on rewrite.
	if err := os.Chmod(path, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// shellQuote single-quotes a string for safe use in /bin/sh. Endpoints
// are pre-validated to contain no quotes; binary paths may contain
// spaces, which single-quoting handles.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// All returns every known agent-browser wirer.
func All() []Wirer {
	return []Wirer{NewBrowserUse(), NewAgentBrowser()}
}

// Lookup returns the wirer with the given Name, or (nil, false).
func Lookup(name string) (Wirer, bool) {
	for _, w := range All() {
		if w.Name() == name {
			return w, true
		}
	}
	return nil, false
}
