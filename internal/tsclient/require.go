package tsclient

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Sentinel errors returned by RequireTailnetIP. Callers should use
// errors.Is to distinguish reasons so the calling CLI can print a
// remediation tailored to the missing piece.
var (
	// ErrTailscaleNotRunning is returned when the Tailscale CLI is
	// missing, or `tailscale status --json` fails, or the daemon
	// reports no Self peer. All three flow through this error so
	// the caller can give one consistent remediation.
	ErrTailscaleNotRunning = errors.New("tsclient: Tailscale not running")

	// ErrNoTailnetInterface is returned when the Tailscale daemon is
	// reachable but no 100.64.0.0/10 IPv4 address is bound on any
	// local network interface. Distinct from ErrTailscaleNotRunning
	// because the user has to take a different action: bring the
	// tailnet interface up, not start the daemon.
	ErrNoTailnetInterface = errors.New("tsclient: no Tailscale 100.x address on a local interface")

	// ErrAmbiguousTailnetIP is returned when more than one 100.x
	// IPv4 address is bound locally. Rare in practice (it happens
	// on multi-tailnet machines or when an old tailnet interface
	// did not clean up) but ambiguous: agentcookie refuses to pick.
	// The caller should ask the user to pin a single IP in
	// sink.yaml or source.yaml.
	ErrAmbiguousTailnetIP = errors.New("tsclient: multiple Tailscale 100.x addresses present; cannot pick one")
)

// remediation is the user-facing block appended to every error message
// returned by RequireTailnetIP. It names the two diagnostic surfaces
// the user can reach: the tailscale CLI for daemon state, and the
// quickstart doc for the wider install ceremony.
const remediation = "run `tailscale status` to inspect the daemon, then re-run; see docs/quickstart.md"

// interfaceAddrsFunc is the network-interface enumerator
// RequireTailnetIP calls. Overridable for tests; production callers
// should leave it alone.
var interfaceAddrsFunc = net.InterfaceAddrs

// RequireTailnetIP returns this machine's Tailscale 100.x IPv4 address
// or a structured error explaining which piece is missing. agentcookie's
// sink and pair listeners call this at startup and refuse to bind on
// 0.0.0.0 or any non-tailnet interface; the wizard install path calls
// this at config-write time and refuses to write sink.yaml on error
// rather than silently falling through to a permissive default. S1 in
// the v0.12 threat survey.
//
// The ctx is reserved for future expansion (probing the daemon over
// `tailscale status --json`). Today RequireTailnetIP only consults
// local interface state, which is what a Tailscale daemon ultimately
// writes anyway, so a ctx parameter is taken to keep the signature
// stable.
func RequireTailnetIP(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_ = ctx // reserved for the daemon-probe expansion (see doc comment)

	addrs, err := interfaceAddrsFunc()
	if err != nil {
		// Enumerating local interfaces should not fail on a healthy
		// macOS box. If it does, surface it as "not running" so the
		// caller gives the same remediation block.
		return "", fmt.Errorf("%w: enumerate interfaces: %v; %s", ErrTailscaleNotRunning, err, remediation)
	}

	var found []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		// Tailscale's CGNAT block is 100.64.0.0/10, i.e. 100.64.0.0
		// through 100.127.255.255. The first octet check is cheap
		// and the second octet bound matches the prefix exactly.
		if ip4[0] != 100 || ip4[1] < 64 || ip4[1] > 127 {
			continue
		}
		found = append(found, ip4.String())
	}

	switch len(found) {
	case 0:
		return "", fmt.Errorf("%w; %s", ErrNoTailnetInterface, remediation)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("%w: candidates=%v; pin one in sink.yaml (listen.addr) or source.yaml; %s", ErrAmbiguousTailnetIP, found, remediation)
	}
}

// IsTailnetIP reports whether s parses as a 100.64.0.0/10 IPv4 address
// (Tailscale's CGNAT range). Used by the sink and pair listener startup
// guards to confirm the configured listen address is actually on the
// tailnet before binding. "localhost"/"127.0.0.1" callers should test
// that separately (it is a legitimate local-dev binding, not a
// tailnet binding).
func IsTailnetIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}
