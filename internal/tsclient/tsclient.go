// Package tsclient probes Tailscale state on the local machine. agentcookie
// uses it during wizard install to detect whether Tailscale is available and
// to advise the user on the exit-node setup that keeps the sink machine's
// outbound IP aligned with the source machine.
package tsclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// macAppCLI is the canonical Tailscale CLI path on a macOS install of
// Tailscale.app. The standalone CLI install drops a tailscale binary on PATH
// instead, which we probe first.
const macAppCLI = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// ErrNotInstalled is returned by FindCLI when no Tailscale binary is on PATH
// or under /Applications/.
var ErrNotInstalled = errors.New("tsclient: Tailscale CLI not found")

// Status is a thin JSON view over `tailscale status --json` containing only
// the fields agentcookie cares about. The real JSON has many more fields.
type Status struct {
	Version string         `json:"Version"`
	Self    *PeerStatus    `json:"Self"`
	Peer    map[string]*PeerStatus `json:"Peer"`
}

// PeerStatus mirrors the per-peer object Tailscale's status JSON emits.
type PeerStatus struct {
	HostName       string   `json:"HostName"`
	DNSName        string   `json:"DNSName"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	ExitNodeOption bool     `json:"ExitNodeOption"`
	ExitNode       bool     `json:"ExitNode"`
	Online         bool     `json:"Online"`
}

// FindCLI returns the path to the local Tailscale CLI. Checks PATH first,
// then the macOS app bundle path. Returns ErrNotInstalled when neither
// exists.
func FindCLI() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	if _, err := os.Stat(macAppCLI); err == nil {
		return macAppCLI, nil
	}
	return "", ErrNotInstalled
}

// Get runs `tailscale status --json` and parses the result. Caller must
// pass a non-empty cliPath (use FindCLI).
func Get(ctx context.Context, cliPath string) (*Status, error) {
	if cliPath == "" {
		return nil, errors.New("tsclient: cliPath is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, cliPath, "status", "--json").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("tsclient: %s status: %w (%s)", cliPath, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("tsclient: %s status: %w", cliPath, err)
	}
	var st Status
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("tsclient: parse status JSON: %w", err)
	}
	return &st, nil
}

// FindPeer returns the PeerStatus for the named hostname. Matches against
// HostName and the DNS-name's first label (Tailscale's "magic DNS" form).
// Returns nil when no peer matches.
func (s *Status) FindPeer(hostname string) *PeerStatus {
	if s == nil {
		return nil
	}
	target := strings.ToLower(strings.TrimSpace(hostname))
	for _, p := range s.Peer {
		if p == nil {
			continue
		}
		if strings.EqualFold(p.HostName, target) {
			return p
		}
		if label, _, ok := strings.Cut(p.DNSName, "."); ok && strings.EqualFold(label, target) {
			return p
		}
	}
	return nil
}

// SelfAdvertisesExitNode reports whether the local machine is configured to
// advertise itself as a Tailscale exit node.
func (s *Status) SelfAdvertisesExitNode() bool {
	if s == nil || s.Self == nil {
		return false
	}
	return s.Self.ExitNodeOption
}

// SelfUsesExitNode reports whether the local machine is currently routing
// its outbound traffic through a Tailscale exit node.
func (s *Status) SelfUsesExitNode() bool {
	if s == nil || s.Self == nil {
		return false
	}
	return s.Self.ExitNode
}

// SelfHostname returns the local machine's tailnet hostname, or empty when
// status is not available.
func (s *Status) SelfHostname() string {
	if s == nil || s.Self == nil {
		return ""
	}
	return s.Self.HostName
}
