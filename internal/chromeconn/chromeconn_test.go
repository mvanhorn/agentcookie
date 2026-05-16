package chromeconn

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseDevToolsActivePort_Happy(t *testing.T) {
	ep, err := parseDevToolsActivePort([]byte("53747\n/devtools/browser/fbaf41a0-51c2-4c3d-86c0-65a7e5db6035\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Port != 53747 {
		t.Errorf("port: got %d, want 53747", ep.Port)
	}
	if ep.WSPath != "/devtools/browser/fbaf41a0-51c2-4c3d-86c0-65a7e5db6035" {
		t.Errorf("wsPath: got %q", ep.WSPath)
	}
	if got := ep.WebSocketURL(); got != "ws://127.0.0.1:53747/devtools/browser/fbaf41a0-51c2-4c3d-86c0-65a7e5db6035" {
		t.Errorf("WebSocketURL: got %q", got)
	}
}

func TestParseDevToolsActivePort_Failures(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"one line only", "53747\n"},
		{"port not a number", "abc\n/devtools/browser/x\n"},
		{"port zero", "0\n/devtools/browser/x\n"},
		{"port too high", "70000\n/devtools/browser/x\n"},
		{"ws path missing slash", "53747\ndevtools/browser/x\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseDevToolsActivePort([]byte(tc.in)); err == nil {
				t.Errorf("expected error for input %q", tc.in)
			}
		})
	}
}

func TestDiscover_FileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := Discover(dir)
	if !errors.Is(err, ErrRemoteDebuggingNotEnabled) {
		t.Errorf("expected ErrRemoteDebuggingNotEnabled, got %v", err)
	}
}

func TestDiscover_HappyPath(t *testing.T) {
	dir := t.TempDir()
	body := "61423\n/devtools/browser/abc-123\n"
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ep, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if ep.Port != 61423 || ep.WSPath != "/devtools/browser/abc-123" {
		t.Errorf("endpoint: %+v", ep)
	}
}

func TestDiscover_EmptyDirRejected(t *testing.T) {
	if _, err := Discover(""); err == nil {
		t.Error("expected error for empty user-data-dir")
	}
}

func TestProbeReachable_Live(t *testing.T) {
	// Start a TCP listener on a random port to simulate Chrome's CDP socket.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := ProbeReachable(ctx, Endpoint{Port: port, WSPath: "/devtools/browser/x"}); err != nil {
		t.Errorf("expected ProbeReachable to succeed, got %v", err)
	}
}

func TestProbeReachable_Stale(t *testing.T) {
	// Bind, capture port, close. The port is now "stale".
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = ProbeReachable(ctx, Endpoint{Port: port, WSPath: "/x"})
	if err == nil {
		t.Fatal("expected stale endpoint error")
	}
	if !errors.Is(err, ErrStaleEndpoint) {
		t.Errorf("expected ErrStaleEndpoint, got %v", err)
	}
}

func TestDefaultProfileDir_Shape(t *testing.T) {
	// Just check the function returns a non-empty path on this OS. Doesn't
	// assert the directory exists since it might not on test runners.
	if got := DefaultProfileDir(); got == "" {
		t.Error("DefaultProfileDir returned empty")
	}
}
