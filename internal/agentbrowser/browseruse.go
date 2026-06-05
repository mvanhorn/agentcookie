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

// cdpURL resolves the AttachTarget to a browser-use --cdp-url value.
// It prefers the stable loopback http:// endpoint (http://127.0.0.1:port)
// over the discovered ws:// browser endpoint: the ws URL embeds a
// per-session devtools/browser/<id> that changes on every Chrome restart,
// so baking it into a durable launcher would break after a restart. The
// http endpoint is stable and browser-use resolves the live ws itself.
func cdpURL(t AttachTarget) (string, error) {
	if t.Port > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", t.Port), nil
	}
	if t.WSEndpoint != "" {
		return t.WSEndpoint, nil
	}
	return "", fmt.Errorf("agentbrowser: AttachTarget has neither Port nor WSEndpoint")
}

// Wire writes a launcher at ~/.agentcookie/agent-browser/browser-use-attached
// that execs `browser-use --cdp-url <url> "$@"`, so any invocation of that
// launcher attaches to the user's real Chrome.
func (b *BrowserUse) Wire(target AttachTarget) (WireResult, error) {
	url, err := cdpURL(target)
	if err != nil {
		return WireResult{}, err
	}
	if err := validateEndpoint(url); err != nil {
		return WireResult{}, err
	}
	dir, err := launcherDir()
	if err != nil {
		return WireResult{}, err
	}
	path, err := writeLauncher(dir, "browser-use-attached", b.binary, []string{"--cdp-url", url})
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
func (b *BrowserUse) LaunchSnippet(target AttachTarget) string {
	url, err := cdpURL(target)
	if err != nil {
		return "browser-use --connect"
	}
	return fmt.Sprintf("browser-use --cdp-url %s   # or: browser-use --connect", url)
}
