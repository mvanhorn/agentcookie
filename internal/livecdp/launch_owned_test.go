package livecdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOwnedLaunchRejectsOccupiedPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"Browser":"unrelated"}`)) }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	_, err := LaunchOwnedChromeOptions(context.Background(), "/bin/false", filepath.Join(t.TempDir(), "profile"), port, LaunchOptions{NoStartupWindow: true})
	if err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("unrelated endpoint accepted: %v", err)
	}
}
func TestOwnedLaunchDetectsEarlyExit(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err = LaunchOwnedChromeOptions(ctx, "/bin/false", filepath.Join(t.TempDir(), "profile"), port, LaunchOptions{NoStartupWindow: true})
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("early exit: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("waited despite child exit")
	}
}

func TestOwnedReceiptCannotAdoptUnrelatedEndpoint(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/other"}`, r.Host)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	data, _ := json.Marshal(ownedReceipt{PID: os.Getpid(), Profile: dir, Endpoint: srv.URL, WebSocket: "ws://expected/devtools/browser/owned"})
	if err := os.WriteFile(ownedReceiptPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumeOwnedChrome(context.Background(), dir, port); err == nil || !strings.Contains(err.Error(), "incarnation") {
		t.Fatalf("unrelated endpoint adopted: %v", err)
	}
}

func TestAdoptedCloseStopsMonitorWhenProofFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not owned", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	done := make(chan struct{})
	o := &OwnedChrome{adopted: true, Endpoint: srv.URL, UserDataDir: t.TempDir(), done: done, monitorCancel: func() { close(done) }}
	if err := o.Close(); err == nil {
		t.Fatal("unverified browser was closed")
	}
	select {
	case <-done:
	default:
		t.Fatal("monitor leaked on verification error")
	}
}

func TestReadOnlyOwnedVerificationPreservesMissingAndStaleReceipts(t *testing.T) {
	dir := t.TempDir()
	if err := VerifyOwnedChrome(context.Background(), dir, 21345); !os.IsNotExist(err) {
		t.Fatalf("missing receipt: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("verification created files")
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/replacement"}`, r.Host)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	data, _ := json.Marshal(ownedReceipt{PID: os.Getpid(), Profile: dir, Endpoint: srv.URL, WebSocket: "ws://expected/devtools/browser/owned"})
	path := ownedReceiptPath(dir)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyOwnedChrome(context.Background(), dir, port); err == nil || !strings.Contains(err.Error(), "incarnation") {
		t.Fatalf("replacement accepted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("verification changed receipt")
	}
	if requests != 1 {
		t.Fatalf("unexpected follow-up monitor/probe: %d requests", requests)
	}
}
