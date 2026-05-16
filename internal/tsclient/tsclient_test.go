package tsclient

import (
	"encoding/json"
	"testing"
)

func TestStatus_ParseSampleJSON(t *testing.T) {
	// Sample shape from `tailscale status --json` on a real tailnet,
	// trimmed to the fields tsclient consumes.
	raw := `{
	  "Version": "1.96.5",
	  "Self": {
	    "HostName": "matts-mac-mini",
	    "DNSName": "matts-mac-mini.tail-xxxx.ts.net.",
	    "TailscaleIPs": ["100.80.229.80"],
	    "ExitNodeOption": false,
	    "ExitNode": false,
	    "Online": true
	  },
	  "Peer": {
	    "abc123": {
	      "HostName": "MacBook Pro (44)",
	      "DNSName": "macbook-pro-44.tail-xxxx.ts.net.",
	      "TailscaleIPs": ["100.98.176.68"],
	      "ExitNodeOption": true,
	      "ExitNode": false,
	      "Online": true
	    }
	  }
	}`
	var st Status
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if st.SelfHostname() != "matts-mac-mini" {
		t.Errorf("self hostname: got %q", st.SelfHostname())
	}
	if st.SelfAdvertisesExitNode() {
		t.Error("self should not advertise exit-node")
	}
	if st.SelfUsesExitNode() {
		t.Error("self should not be using exit-node")
	}

	p := st.FindPeer("macbook-pro-44")
	if p == nil {
		t.Fatal("expected to find peer by DNS label")
	}
	if !p.ExitNodeOption {
		t.Error("peer should advertise exit-node")
	}
	if p.TailscaleIPs[0] != "100.98.176.68" {
		t.Errorf("peer IP: got %v", p.TailscaleIPs)
	}
}

func TestFindPeer_Misses(t *testing.T) {
	st := &Status{Peer: map[string]*PeerStatus{
		"a": {HostName: "alpha", DNSName: "alpha.example.ts.net."},
	}}
	if got := st.FindPeer("zulu"); got != nil {
		t.Errorf("expected nil for missing peer, got %v", got)
	}
	if got := (*Status)(nil).FindPeer("alpha"); got != nil {
		t.Errorf("nil receiver should return nil, got %v", got)
	}
}
