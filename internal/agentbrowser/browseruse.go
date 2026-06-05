package agentbrowser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// browserUseFallbackBinary is where uv installs the browser-use CLI when
// it is not otherwise on PATH.
func browserUseFallbackBinary() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "browser-use")
}

// BrowserUse wires the browser-use CLI onto a CDP endpoint. browser-use
// takes the endpoint as a launch flag (`--cdp-url`, or `--connect` to
// auto-discover), and its skill_cli daemon has no persisted cdp config
// key, so durable wiring is a launcher wrapper rather than a config edit.
type BrowserUse struct {
	binary string
}

// NewBrowserUse resolves the browser-use binary (PATH, then the uv
// fallback) and returns an adapter.
func NewBrowserUse() *BrowserUse {
	bin := browserUseFallbackBinary()
	if p, err := exec.LookPath("browser-use"); err == nil {
		bin = p
	}
	return &BrowserUse{binary: bin}
}

// newBrowserUseAt is the test constructor with an explicit binary path.
func newBrowserUseAt(binary string) *BrowserUse {
	return &BrowserUse{binary: binary}
}

func (b *BrowserUse) Name() string { return "browser-use" }

func (b *BrowserUse) BinaryPath() string { return b.binary }

func (b *BrowserUse) IsInstalled() bool {
	info, err := os.Stat(b.binary)
	return err == nil && !info.IsDir()
}

// Wire writes a launcher at ~/.agentcookie/agent-browser/browser-use-attached
// that execs `browser-use --cdp-url <endpoint> "$@"`, so any invocation
// of that launcher attaches to the user's real Chrome.
func (b *BrowserUse) Wire(endpoint string) (WireResult, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return WireResult{}, err
	}
	dir, err := launcherDir()
	if err != nil {
		return WireResult{}, err
	}
	path, err := writeLauncher(dir, "browser-use-attached", b.binary, []string{"--cdp-url", endpoint})
	if err != nil {
		return WireResult{}, err
	}
	return WireResult{
		LauncherPath: path,
		Note:         fmt.Sprintf("Run %s to drive browser-use attached to your real Chrome.", path),
	}, nil
}

// LaunchSnippet returns the one-shot command for attaching without
// writing a launcher. `--connect` (zero-arg auto-discovery) is offered as
// the alternative that survives a changed debug port.
func (b *BrowserUse) LaunchSnippet(endpoint string) string {
	return fmt.Sprintf("browser-use --cdp-url %s   # or: browser-use --connect", endpoint)
}
