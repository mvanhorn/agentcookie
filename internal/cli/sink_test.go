package cli

import (
	"strings"
	"testing"
)

// TestValidateListenAddr_PolicyMatrix exercises the v0.12 S1 binding
// policy enforced by validateListenAddr. The runtime sink startup
// guard and the wizard's --listen flag both call this; one table
// keeps the two callers honest about identical semantics.
func TestValidateListenAddr_PolicyMatrix(t *testing.T) {
	cases := []struct {
		name      string
		addr      string
		wantErr   bool
		wantInMsg string // substring asserted when wantErr is true
	}{
		// Refused: any-interface binds.
		{
			name:      "refuses 0.0.0.0",
			addr:      "0.0.0.0:9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},
		{
			name:      "refuses :: (IPv6 any)",
			addr:      "[::]:9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},
		{
			name:      "refuses bare :port (empty host)",
			addr:      ":9999",
			wantErr:   true,
			wantInMsg: "every interface",
		},

		// Refused: non-tailnet routable address.
		{
			name:      "refuses LAN 192.168.x",
			addr:      "192.168.1.5:9999",
			wantErr:   true,
			wantInMsg: "not a Tailscale 100.x address",
		},
		{
			name:      "refuses 100.x but outside CGNAT block",
			addr:      "100.63.0.5:9999",
			wantErr:   true,
			wantInMsg: "not a Tailscale 100.x address",
		},

		// Refused: unparseable input. SplitHostPort is loose about
		// what it accepts as a host token (whitespace is fine), so
		// the test case picks an input it definitively rejects:
		// no port separator.
		{
			name:      "refuses input with no port",
			addr:      "no-colon-here",
			wantErr:   true,
			wantInMsg: "host:port",
		},

		// Accepted: explicit loopback, tailnet 100.x.
		{
			name: "accepts 127.0.0.1 (operator-typed local dev)",
			addr: "127.0.0.1:9999",
		},
		{
			name: "accepts ::1 loopback",
			addr: "[::1]:9999",
		},
		{
			name: "accepts localhost",
			addr: "localhost:9999",
		},
		{
			name: "accepts tailnet 100.80.x",
			addr: "100.80.229.80:9999",
		},
		{
			name: "accepts tailnet boundary 100.64.0.1",
			addr: "100.64.0.1:9999",
		},
		{
			name: "accepts tailnet upper boundary 100.127.255.254",
			addr: "100.127.255.254:9999",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateListenAddr(tc.addr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.addr)
				}
				if !strings.Contains(err.Error(), tc.wantInMsg) {
					t.Errorf("error for %q: got %v, want substring %q", tc.addr, err, tc.wantInMsg)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tc.addr, err)
			}
		})
	}
}

// TestValidateListenAddr_RefusesV011DefaultFallback documents the
// specific regression v0.12 closes. The pre-v0.12 wizard fell through
// to "0.0.0.0:9999" when Tailscale detection failed, and the config
// loader added a second silent fallback to "127.0.0.1:9999" on empty.
// A sink that ends up bound to 0.0.0.0 at runtime must now refuse
// to start so the operator sees the failure rather than serving
// publicly.
func TestValidateListenAddr_RefusesV011DefaultFallback(t *testing.T) {
	err := validateListenAddr("0.0.0.0:9999")
	if err == nil {
		t.Fatal("v0.12: sink listener must refuse 0.0.0.0:9999")
	}
	// Operator-facing message must name the v0.12 remediation surfaces.
	if !strings.Contains(err.Error(), "tailscale status") {
		t.Errorf("error should name `tailscale status`: %v", err)
	}
	if !strings.Contains(err.Error(), "docs/quickstart.md") {
		t.Errorf("error should name docs/quickstart.md: %v", err)
	}
}
