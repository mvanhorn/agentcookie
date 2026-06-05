package agentbrowser

import (
	"fmt"
	"os"
	"os/exec"
)

// agentBrowserFallbackBinary is the Homebrew install location used when
// agent-browser is not otherwise on PATH.
func agentBrowserFallbackBinary() string {
	return "/opt/homebrew/bin/agent-browser"
}

// AgentBrowser wires the vercel-labs agent-browser CLI onto a CDP
// endpoint. agent-browser attaches with `--cdp <port>` (or `--auto-connect`
// to reuse a running Chrome's auth state); it takes the bare debug port,
// not a ws URL, so the adapter wires from AttachTarget.Port.
type AgentBrowser struct {
	binary string
}

// NewAgentBrowser resolves the agent-browser binary (PATH, then the
// Homebrew fallback) and returns an adapter.
func NewAgentBrowser() *AgentBrowser {
	bin := agentBrowserFallbackBinary()
	if p, err := exec.LookPath("agent-browser"); err == nil {
		bin = p
	}
	return &AgentBrowser{binary: bin}
}

// newAgentBrowserAt is the test constructor with an explicit binary path.
func newAgentBrowserAt(binary string) *AgentBrowser {
	return &AgentBrowser{binary: binary}
}

func (a *AgentBrowser) Name() string { return "agent-browser" }

func (a *AgentBrowser) BinaryPath() string { return a.binary }

func (a *AgentBrowser) LauncherPath() string { return launcherPathNoCreate("agent-browser-attached") }

func (a *AgentBrowser) IsInstalled() bool {
	info, err := os.Stat(a.binary)
	return err == nil && !info.IsDir()
}

// Wire writes a launcher at ~/.agentcookie/agent-browser/agent-browser-attached
// that execs `agent-browser --cdp <port> "$@"`. The port is an int, so no
// shell-injection surface exists on this path.
func (a *AgentBrowser) Wire(target AttachTarget) (WireResult, error) {
	if target.Port <= 0 {
		return WireResult{}, fmt.Errorf("agentbrowser: agent-browser needs a debug port (AttachTarget.Port)")
	}
	dir, err := launcherDir()
	if err != nil {
		return WireResult{}, err
	}
	path, err := writeLauncher(dir, "agent-browser-attached", a.binary, []string{"--cdp", fmt.Sprintf("%d", target.Port)})
	if err != nil {
		return WireResult{}, err
	}
	return WireResult{
		LauncherPath: path,
		Note:         fmt.Sprintf("Run %s to drive agent-browser attached to your real Chrome.", path),
	}, nil
}

// LaunchSnippet returns the one-shot attach command. `--auto-connect`
// (reuse a running Chrome) is offered as the zero-port alternative.
func (a *AgentBrowser) LaunchSnippet(target AttachTarget) string {
	if target.Port <= 0 {
		return "agent-browser --auto-connect"
	}
	return fmt.Sprintf("agent-browser --cdp %d   # or: agent-browser --auto-connect", target.Port)
}
