package sinkpush

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// TestCmuxLiveInjection drives the real CmuxAdapter against a running
// cmux. It is opt-in (set AGENTCOOKIE_LIVE_CMUX=1) and must be run from
// a process started inside cmux, because cmux's default socketControlMode
// "cmuxOnly" only accepts cmux-child callers -- which is exactly the
// constraint the sink hits in production (and what the doctor check
// flags). It uses a throwaway test domain, verifies the injected value
// round-trips byte-for-byte, and cleans up its own surface and cookies.
//
//	AGENTCOOKIE_LIVE_CMUX=1 go test ./internal/sinkpush/ -run TestCmuxLiveInjection -v
func TestCmuxLiveInjection(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CMUX") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CMUX=1 (and run inside cmux) to exercise the live cmux path")
	}

	a := NewCmux("", nil)
	if !a.IsInstalled() {
		t.Skipf("cmux CLI not found at %s", a.CLIBinary())
	}

	const (
		// A scoped subdomain of the IANA example domain: WebKit accepts it
		// (unlike a bare reserved TLD), and clear-by-url stays narrow to
		// this host so we never touch unrelated cookies.
		host    = ".livetest.example.com"
		url     = "https://livetest.example.com/"
		longVal = "0123456789abcdef0123456789abcdef" + "LIVE_PAYLOAD_AFTER_32_BYTES"
	)
	// ExpiresUTC is Chrome micros-since-1601 for 2027-01-01 (a future
	// date; WebKit clamps to its ~400-day max but keeps it persistent).
	const future2027Micros = 13443235200000000
	cookies := []chrome.Cookie{
		{HostKey: host, Name: "ac_live_session", Value: longVal, Path: "/", IsSecure: 1, SameSite: 1, ExpiresUTC: future2027Micros},
		{HostKey: host, Name: "ac_live_temp", Value: "sessioncookie", Path: "/"}, // no expiry -> session
	}

	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push to live cmux failed: %v", err)
	}
	t.Cleanup(func() {
		if a.surfaceID == "" {
			return
		}
		_, _ = a.run("browser", "--surface", a.surfaceID, "cookies", "clear", "--url", url)
		_, _ = a.run("rpc", "surface.close", `{"surface_id":"`+a.surfaceID+`"}`)
	})

	if a.surfaceID == "" {
		t.Fatal("adapter did not cache a surface after Push")
	}

	out, err := a.run("rpc", "browser.cookies.get", `{"surface_id":"`+a.surfaceID+`","url":"`+url+`"}`)
	if err != nil {
		t.Fatalf("cookies.get: %v", err)
	}
	var got struct {
		Cookies []struct {
			Name        string `json:"name"`
			Value       string `json:"value"`
			Domain      string `json:"domain"`
			SessionOnly bool   `json:"session_only"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse cookies.get output %q: %v", out, err)
	}

	byName := map[string]struct {
		value       string
		sessionOnly bool
	}{}
	for _, c := range got.Cookies {
		byName[c.Name] = struct {
			value       string
			sessionOnly bool
		}{c.Value, c.SessionOnly}
	}

	sess, ok := byName["ac_live_session"]
	if !ok {
		t.Fatalf("ac_live_session not found in cmux after injection; got %d cookies for the domain", len(got.Cookies))
	}
	if sess.value != longVal {
		t.Errorf("value not preserved (App-Bound double-strip regression?).\n got: %q\nwant: %q", sess.value, longVal)
	}
	if sess.sessionOnly {
		t.Errorf("ac_live_session had an expiry; should be persistent, got session_only=true")
	}
	if temp, ok := byName["ac_live_temp"]; !ok {
		t.Error("ac_live_temp (session cookie) not found after injection")
	} else if !temp.sessionOnly {
		t.Error("ac_live_temp had no expiry; expected session_only=true")
	}

	t.Logf("live injection OK: %d cookies landed in cmux surface %s, value round-tripped byte-for-byte", len(got.Cookies), a.surfaceID)
}
