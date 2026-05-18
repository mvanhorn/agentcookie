package tsclient

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// withFakeAddrs swaps interfaceAddrsFunc for the duration of a test.
// All cases must mutate it through this helper so cleanup happens via
// t.Cleanup even on a t.Fatal.
func withFakeAddrs(t *testing.T, addrs []net.Addr, err error) {
	t.Helper()
	prev := interfaceAddrsFunc
	t.Cleanup(func() { interfaceAddrsFunc = prev })
	interfaceAddrsFunc = func() ([]net.Addr, error) { return addrs, err }
}

// ipnet builds a *net.IPNet from a CIDR string for the test tables.
// Anything malformed in a test is a test bug, so t.Fatal.
func ipnet(t *testing.T, cidr string) net.Addr {
	t.Helper()
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parse %s: %v", cidr, err)
	}
	ipNet.IP = ip // preserve the host octets, not the network prefix
	return ipNet
}

func TestRequireTailnetIP(t *testing.T) {
	cases := []struct {
		name     string
		addrs    []net.Addr
		addrsErr error
		wantIP   string
		wantErr  error // sentinel checked via errors.Is
	}{
		{
			name: "happy path: one 100.x address",
			addrs: []net.Addr{
				ipnet(t, "192.168.1.42/24"),
				ipnet(t, "100.80.229.80/32"),
				ipnet(t, "fe80::1/64"),
			},
			wantIP: "100.80.229.80",
		},
		{
			name: "edge: only non-tailnet interfaces; daemon down / interface down",
			addrs: []net.Addr{
				ipnet(t, "192.168.1.42/24"),
				ipnet(t, "10.0.0.5/8"),
				ipnet(t, "fe80::1/64"),
			},
			wantErr: ErrNoTailnetInterface,
		},
		{
			name:    "edge: empty interface list",
			addrs:   []net.Addr{},
			wantErr: ErrNoTailnetInterface,
		},
		{
			name: "edge: 100.x but outside CGNAT block is not tailnet",
			addrs: []net.Addr{
				// 100.0.0.0/8 around the CGNAT block: 100.63 and 100.128 are NOT
				// tailnet space. agentcookie must not accept them.
				ipnet(t, "100.63.0.5/32"),
				ipnet(t, "100.128.0.5/32"),
			},
			wantErr: ErrNoTailnetInterface,
		},
		{
			name: "edge: multiple 100.x addresses present",
			addrs: []net.Addr{
				ipnet(t, "100.80.229.80/32"),
				ipnet(t, "100.98.176.68/32"),
			},
			wantErr: ErrAmbiguousTailnetIP,
		},
		{
			name:     "edge: interface enumeration fails",
			addrsErr: errors.New("syscall failed"),
			wantErr:  ErrTailscaleNotRunning,
		},
		{
			name: "edge: ignore IPv6 100:: addresses (different family)",
			addrs: []net.Addr{
				// IPv6 address that starts with 100 in text but is in a totally
				// different range; must not be confused with the IPv4 CGNAT block.
				ipnet(t, "100::1/128"),
			},
			wantErr: ErrNoTailnetInterface,
		},
		{
			name: "edge: 100.64.x boundary is tailnet",
			addrs: []net.Addr{
				ipnet(t, "100.64.0.1/32"),
			},
			wantIP: "100.64.0.1",
		},
		{
			name: "edge: 100.127.x upper boundary is tailnet",
			addrs: []net.Addr{
				ipnet(t, "100.127.255.254/32"),
			},
			wantIP: "100.127.255.254",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeAddrs(t, tc.addrs, tc.addrsErr)
			got, err := RequireTailnetIP(context.Background())
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil (ip=%q)", tc.wantErr, got)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("error: got %v, want sentinel %v", err, tc.wantErr)
				}
				// Every error message must name the remediation surfaces
				// agentcookie users need to reach. This protects S1's
				// "no silent fallback" property at the error-message
				// level: a user sees the exact command and doc to consult.
				if !strings.Contains(err.Error(), "tailscale status") {
					t.Errorf("error should name `tailscale status`, got %v", err)
				}
				if !strings.Contains(err.Error(), "docs/quickstart.md") {
					t.Errorf("error should name docs/quickstart.md, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantIP {
				t.Errorf("ip: got %q, want %q", got, tc.wantIP)
			}
		})
	}
}

func TestRequireTailnetIP_NilContext(t *testing.T) {
	// Defensive: callers passing a nil context (a mistake) should not
	// crash; RequireTailnetIP swaps a Background in. Verified by happy-path
	// shape, since today no ctx-respecting path runs.
	withFakeAddrs(t, []net.Addr{ipnet(t, "100.80.229.80/32")}, nil)
	got, err := RequireTailnetIP(nil) //nolint:staticcheck // intentional nil ctx
	if err != nil {
		t.Fatal(err)
	}
	if got != "100.80.229.80" {
		t.Errorf("got %q", got)
	}
}

func TestIsTailnetIP(t *testing.T) {
	cases := map[string]bool{
		"100.80.229.80":  true,
		"100.64.0.0":     true,
		"100.127.255.255": true,
		"100.63.0.0":     false, // one below the block
		"100.128.0.0":    false, // one above the block
		"127.0.0.1":      false, // loopback, not tailnet
		"0.0.0.0":        false, // any-interface bind, definitely not tailnet
		"192.168.1.1":    false,
		"":               false,
		"not an ip":      false,
		"::1":            false, // IPv6 loopback
		"100::1":         false, // IPv6 unrelated
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := IsTailnetIP(in); got != want {
				t.Errorf("IsTailnetIP(%q) = %v, want %v", in, got, want)
			}
		})
	}
}
