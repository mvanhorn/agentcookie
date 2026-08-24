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
	"net"
	"net/url"
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
	Version string                 `json:"Version"`
	Self    *PeerStatus            `json:"Self"`
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

// ErrPeerNotFound is returned by ResolvePeerIP when no matching peer exists.
var ErrPeerNotFound = errors.New("tsclient: peer not found")

// ErrPeerOffline is returned by ResolvePeerIP when all matching peers are offline.
var ErrPeerOffline = errors.New("tsclient: peer is offline")

// ErrPeerNoIPv4 is returned when a peer has no IPv4 address in TailscaleIPs.
var ErrPeerNoIPv4 = errors.New("tsclient: peer has no IPv4 address")

// ErrAmbiguousPeer is returned when multiple Online peers share the same
// hostname. The caller should pin sink.url to a specific 100.x IP or delete
// the leftover node from the Tailscale admin console.
var ErrAmbiguousPeer = errors.New("tsclient: multiple online peers match hostname")

// ResolvePeerIP resolves a Tailscale hostname to its 100.x IPv4 address.
// When multiple peers share the same hostname (a common scenario after
// Tailscale re-auth creates a new node while the old offline node lingers),
// this function prefers Online peers over offline duplicates.
//
// The hostname can be:
//   - Short MagicDNS name: "grok-bot"
//   - Full MagicDNS FQDN: "grok-bot.tail-xxxx.ts.net."
//   - HostName field value: "grok-bot-1"
//
// Returns ErrPeerNotFound if no peer matches, ErrPeerOffline if all matches
// are offline, ErrAmbiguousPeer if multiple Online peers match (caller should
// pin sink.url to a 100.x IP or delete the leftover node), or ErrPeerNoIPv4
// if the peer lacks an IPv4 address.
func (s *Status) ResolvePeerIP(hostname string) (string, error) {
	if s == nil {
		return "", ErrPeerNotFound
	}

	target := strings.ToLower(strings.TrimSpace(hostname))
	// Strip trailing dot from FQDN if present
	target = strings.TrimSuffix(target, ".")

	var matches []*PeerStatus
	for _, p := range s.Peer {
		if p == nil {
			continue
		}
		// Match against HostName (e.g., "grok-bot-1")
		if strings.EqualFold(p.HostName, target) {
			matches = append(matches, p)
			continue
		}
		// Match against DNS name's first label (MagicDNS short name)
		if label, _, ok := strings.Cut(p.DNSName, "."); ok && strings.EqualFold(label, target) {
			matches = append(matches, p)
			continue
		}
		// Match against full DNSName (with or without trailing dot)
		dnsLower := strings.ToLower(strings.TrimSuffix(p.DNSName, "."))
		if dnsLower == target {
			matches = append(matches, p)
			continue
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("%w: %q", ErrPeerNotFound, hostname)
	}

	// Collect Online peers. If exactly one is online, use it. If multiple
	// are online, fail closed (nondeterministic map iteration order would
	// pick arbitrarily). If none are online, return ErrPeerOffline.
	var onlinePeers []*PeerStatus
	for _, p := range matches {
		if p.Online {
			onlinePeers = append(onlinePeers, p)
		}
	}

	if len(onlinePeers) == 0 {
		return "", fmt.Errorf("%w: %q (all %d matching nodes are offline; check `tailscale status`)", ErrPeerOffline, hostname, len(matches))
	}

	if len(onlinePeers) > 1 {
		// Multiple online peers with the same hostname. Collect their IPs
		// for the error message so the operator can pick one.
		var ips []string
		for _, p := range onlinePeers {
			for _, ip := range p.TailscaleIPs {
				if IsTailnetIP(ip) {
					ips = append(ips, ip)
					break
				}
			}
		}
		return "", fmt.Errorf("%w: %q has %d online nodes with IPs %v; pin sink.url to one 100.x IP or delete the leftover node from Tailscale admin",
			ErrAmbiguousPeer, hostname, len(onlinePeers), ips)
	}

	// Exactly one online peer - use it
	best := onlinePeers[0]

	// Extract IPv4 from TailscaleIPs
	for _, ip := range best.TailscaleIPs {
		if IsTailnetIP(ip) {
			return ip, nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrPeerNoIPv4, hostname)
}

// ResolvePeerIPWithCLI is a convenience wrapper that finds the Tailscale CLI,
// gets the current status, and resolves the hostname. Use this when you don't
// already have a Status object.
func ResolvePeerIPWithCLI(ctx context.Context, hostname string) (string, error) {
	cli, err := FindCLI()
	if err != nil {
		return "", err
	}
	st, err := Get(ctx, cli)
	if err != nil {
		return "", err
	}
	return st.ResolvePeerIP(hostname)
}

// ResolveSinkURL takes a sink URL and resolves any hostname in it to a
// Tailscale IP, preferring Online peers. If the URL already uses an IP
// address, it is returned unchanged. If the hostname cannot be resolved
// (Tailscale not available, peer not found, peer offline), the original
// URL is returned along with an error so the caller can decide whether
// to proceed with the unresolved URL.
//
// Examples:
//   - "http://grok-bot:9999/sync" -> "http://100.124.19.34:9999/sync"
//   - "http://grok-bot.tail-xxxx.ts.net:9999/sync" -> "http://100.124.19.34:9999/sync"
//   - "http://100.87.49.2:9999/sync" -> "http://100.87.49.2:9999/sync" (unchanged)
func ResolveSinkURL(ctx context.Context, rawURL string) (string, error) {
	parsed, err := parseURL(rawURL)
	if err != nil {
		return rawURL, fmt.Errorf("parse sink URL: %w", err)
	}

	host := parsed.Hostname()
	port := parsed.Port()

	// If host is already an IP, return unchanged
	if isIPAddress(host) {
		return rawURL, nil
	}

	// Resolve hostname via Tailscale
	resolvedIP, err := ResolvePeerIPWithCLI(ctx, host)
	if err != nil {
		return rawURL, err
	}

	// Reconstruct the URL with the resolved IP
	newHost := resolvedIP
	if port != "" {
		newHost = resolvedIP + ":" + port
	}
	parsed.Host = newHost

	return parsed.String(), nil
}

// isIPAddress returns true if s is an IPv4 or IPv6 address literal.
func isIPAddress(s string) bool {
	// net.ParseIP returns nil for invalid IPs and for hostnames
	ip := net.ParseIP(s)
	return ip != nil
}

// parseURL is a thin wrapper around url.Parse that handles common edge cases.
func parseURL(rawURL string) (*url.URL, error) {
	return url.Parse(rawURL)
}
