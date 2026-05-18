package cli

import (
	"strings"
	"testing"
)

// TestRenderSinkYAML_WritesResolvedAddr proves the wizard pipes the
// resolved tailnet IP into sink.yaml verbatim. Pre-v0.12 the render
// helper called net.InterfaceAddrs directly and fell through to
// 0.0.0.0:9999 on failure; the v0.12 shape takes the address as an
// argument so the call site can call tsclient.RequireTailnetIP and
// fail loud before we ever reach this helper.
func TestRenderSinkYAML_WritesResolvedAddr(t *testing.T) {
	got := renderSinkYAML("my-laptop", "100.80.229.80:9999")
	if !strings.Contains(got, "addr: 100.80.229.80:9999") {
		t.Errorf("expected listen.addr in YAML, got:\n%s", got)
	}
	if !strings.Contains(got, "hostname: my-laptop") {
		t.Errorf("expected peer.hostname in YAML, got:\n%s", got)
	}
	if strings.Contains(got, "0.0.0.0") {
		t.Errorf("v0.12: sink.yaml must never carry 0.0.0.0; got:\n%s", got)
	}
}

// TestValidateListenAddr_AcceptsExplicitOperatorInput is the regression
// guard for the wizard's --listen flag. An operator passing an
// explicit value (during local dev or for an unusual deployment) must
// be allowed through if it matches the policy. The empty-flag path is
// the one that auto-detects; this test covers the explicit path.
func TestValidateListenAddr_AcceptsExplicitOperatorInput(t *testing.T) {
	ok := []string{
		"100.80.229.80:9998",
		"127.0.0.1:9998",
		"localhost:9998",
	}
	for _, addr := range ok {
		if err := validateListenAddr(addr); err != nil {
			t.Errorf("validateListenAddr(%q) unexpectedly errored: %v", addr, err)
		}
	}

	refused := map[string]string{
		"0.0.0.0:9998":   "every interface",
		"192.168.1.5:9998": "not a Tailscale 100.x address",
	}
	for addr, want := range refused {
		err := validateListenAddr(addr)
		if err == nil {
			t.Errorf("validateListenAddr(%q) should have errored", addr)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateListenAddr(%q): error %v, want substring %q", addr, err, want)
		}
	}
}
