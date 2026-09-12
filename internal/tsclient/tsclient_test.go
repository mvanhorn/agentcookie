package tsclient

import (
	"encoding/json"
	"errors"
	"net/url"
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

func TestResolvePeerIP(t *testing.T) {
	cases := []struct {
		name     string
		status   *Status
		hostname string
		wantIP   string
		wantErr  error
	}{
		{
			name: "online peer by hostname",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantIP:   "100.124.19.34",
		},
		{
			name: "online peer by MagicDNS short name",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot-1", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantIP:   "100.124.19.34",
		},
		{
			name: "online peer by full FQDN",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot-1", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot.tail-xxxx.ts.net.",
			wantIP:   "100.124.19.34",
		},
		{
			name: "online peer by FQDN without trailing dot",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot-1", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot.tail-xxxx.ts.net",
			wantIP:   "100.124.19.34",
		},
		{
			name: "duplicate hostname: prefer online over offline",
			status: &Status{Peer: map[string]*PeerStatus{
				"stale": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.87.49.2"}, Online: false},
				"live":  {HostName: "grok-bot-1", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantIP:   "100.124.19.34",
		},
		{
			name: "duplicate hostname both share MagicDNS: prefer online",
			status: &Status{Peer: map[string]*PeerStatus{
				"old": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.87.49.2"}, Online: false},
				"new": {HostName: "grok-bot", DNSName: "grok-bot-1.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantIP:   "100.124.19.34",
		},
		{
			name: "all matching peers offline",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.87.49.2"}, Online: false},
				"b": {HostName: "grok-bot", DNSName: "grok-bot-2.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.87.49.3"}, Online: false},
			}},
			hostname: "grok-bot",
			wantErr:  ErrPeerOffline,
		},
		{
			name: "ambiguous: multiple online peers same hostname",
			status: &Status{Peer: map[string]*PeerStatus{
				"node1": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.87.49.2"}, Online: true},
				"node2": {HostName: "grok-bot", DNSName: "grok-bot-2.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantErr:  ErrAmbiguousPeer,
		},
		{
			name: "peer not found",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "alpha", DNSName: "alpha.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.80.229.80"}, Online: true},
			}},
			hostname: "grok-bot",
			wantErr:  ErrPeerNotFound,
		},
		{
			name:     "nil status",
			status:   nil,
			hostname: "grok-bot",
			wantErr:  ErrPeerNotFound,
		},
		{
			name: "peer online but no IPv4",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"fd7a:115c:a1e0::1"}, Online: true},
			}},
			hostname: "grok-bot",
			wantErr:  ErrPeerNoIPv4,
		},
		{
			name: "case insensitive match",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "Grok-Bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
			}},
			hostname: "GROK-BOT",
			wantIP:   "100.124.19.34",
		},
		{
			name: "peer with multiple IPs picks first IPv4",
			status: &Status{Peer: map[string]*PeerStatus{
				"a": {HostName: "grok-bot", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"fd7a:115c:a1e0::1", "100.124.19.34"}, Online: true},
			}},
			hostname: "grok-bot",
			wantIP:   "100.124.19.34",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.status.ResolvePeerIP(tc.hostname)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil (ip=%q)", tc.wantErr, got)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("error: got %v, want sentinel %v", err, tc.wantErr)
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

func TestIsIPAddress(t *testing.T) {
	cases := map[string]bool{
		"100.80.229.80":             true,
		"192.168.1.1":               true,
		"127.0.0.1":                 true,
		"0.0.0.0":                   true,
		"::1":                       true,
		"fd7a:115c:a1e0::1":         true,
		"grok-bot":                  false,
		"grok-bot.tail-xxxx.ts.net": false,
		"localhost":                 false,
		"":                          false,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := isIPAddress(in); got != want {
				t.Errorf("isIPAddress(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

func TestResolveSinkURL_IPPassthrough(t *testing.T) {
	// When the URL already contains an IP, ResolveSinkURL should return it unchanged.
	// This test doesn't need Tailscale CLI since IP URLs bypass resolution.
	cases := []string{
		"http://100.80.229.80:9999/sync",
		"http://127.0.0.1:9999/sync",
		"http://192.168.1.1:8080/healthz",
	}
	for _, rawURL := range cases {
		t.Run(rawURL, func(t *testing.T) {
			// Even if Tailscale is not available, IP URLs should pass through
			got, err := resolveSinkURLWithStatus(nil, rawURL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != rawURL {
				t.Errorf("got %q, want %q", got, rawURL)
			}
		})
	}
}

func TestResolveSinkURL_HostnameResolution(t *testing.T) {
	st := &Status{Peer: map[string]*PeerStatus{
		"live": {HostName: "grok-bot-1", DNSName: "grok-bot.tail-xxxx.ts.net.", TailscaleIPs: []string{"100.124.19.34"}, Online: true},
	}}

	cases := []struct {
		name    string
		rawURL  string
		wantURL string
	}{
		{
			name:    "short hostname",
			rawURL:  "http://grok-bot:9999/sync",
			wantURL: "http://100.124.19.34:9999/sync",
		},
		{
			name:    "FQDN hostname",
			rawURL:  "http://grok-bot.tail-xxxx.ts.net:9999/sync",
			wantURL: "http://100.124.19.34:9999/sync",
		},
		{
			name:    "preserves path",
			rawURL:  "http://grok-bot:9999/healthz",
			wantURL: "http://100.124.19.34:9999/healthz",
		},
		{
			name:    "preserves query",
			rawURL:  "http://grok-bot:9999/sync?foo=bar",
			wantURL: "http://100.124.19.34:9999/sync?foo=bar",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSinkURLWithStatus(st, tc.rawURL)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantURL {
				t.Errorf("got %q, want %q", got, tc.wantURL)
			}
		})
	}
}

func TestResolveSinkURL_HostnameNotFound(t *testing.T) {
	st := &Status{Peer: map[string]*PeerStatus{}}
	rawURL := "http://unknown-host:9999/sync"

	got, err := resolveSinkURLWithStatus(st, rawURL)
	if err == nil {
		t.Fatal("expected error for unknown hostname")
	}
	if !errors.Is(err, ErrPeerNotFound) {
		t.Errorf("error: got %v, want sentinel %v", err, ErrPeerNotFound)
	}
	// Should return original URL on error
	if got != rawURL {
		t.Errorf("on error, should return original URL: got %q, want %q", got, rawURL)
	}
}

// resolveSinkURLWithStatus is a test helper that resolves a sink URL using a
// pre-populated Status rather than calling the Tailscale CLI.
func resolveSinkURLWithStatus(st *Status, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}

	host := parsed.Hostname()
	port := parsed.Port()

	if isIPAddress(host) {
		return rawURL, nil
	}

	if st == nil {
		return rawURL, nil
	}

	resolvedIP, err := st.ResolvePeerIP(host)
	if err != nil {
		return rawURL, err
	}

	newHost := resolvedIP
	if port != "" {
		newHost = resolvedIP + ":" + port
	}
	parsed.Host = newHost

	return parsed.String(), nil
}
