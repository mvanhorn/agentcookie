//go:build linux

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/cli"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/pkg/sidecar"
)

// Previous release execution is Linux-only. CI verifies the pinned tarball
// checksum before supplying this path. These tests never run old binaries on
// a Mac and never inherit host credentials, PATH tools or browser profiles.
const compatSecret = "agentcookie-synthetic-compatibility-secret-0123456789"

func oldBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("AGENTCOOKIE_OLD_BINARY")
	if binary == "" {
		t.Skip("previous Linux release not supplied")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}
func fixtureEnvironment(h *Harness) []string {
	return []string{"HOME=" + h.Home, "XDG_CONFIG_HOME=" + filepath.Join(h.Home, "config"), "TMPDIR=" + h.Home, "PATH=" + filepath.Join(h.Home, "empty-bin"), "LANG=C", "TZ=UTC"}
}
func writeCompatConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocklist.yaml"), []byte("version: 1\npolicy: allowlist\ndomains:\n  - pattern: '%localhost'\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
func startOldSink(t *testing.T, h *Harness, binary, dir string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	writeCompatConfig(t, dir, "sink.yaml", fmt.Sprintf("listen:\n  addr: %s\nsecurity:\n  shared_secret: %s\nskip_chrome_sqlite: true\nlive_cdp:\n  enabled: false\n", address, compatSecret))
	logFile, err := os.Create(filepath.Join(h.Home, "old-sink.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	cmd := exec.Command(binary, "--config-dir", dir, "sink")
	cmd.Env = fixtureEnvironment(h)
	cmd.Dir = h.Home
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	endpoint := "http://" + address
	deadline := time.Now().Add(15 * time.Second)
	client := http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return endpoint
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	logData, _ := os.ReadFile(logFile.Name())
	t.Fatalf("old sink did not become healthy: %s", logData)
	return ""
}
func TestPreviousV2SinkAcceptsCurrentSource(t *testing.T) {
	binary := oldBinary(t)
	h := New(t)
	endpoint := startOldSink(t, h, binary, filepath.Join(h.Home, "old-sink"))
	cfg := h.ChromiumFixture([]chrome.Cookie{{HostKey: "fixture.localhost", Name: "compat", Value: "synthetic", Path: "/", SameSite: -1}})
	_, push, restore, err := cli.HermeticPipelineForTesting(filepath.Join(h.Home, "config"), compatSecret, func(chrome.Browser) (string, error) { return FixturePassword, nil }, func(context.Context, []chrome.Cookie) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	cfg.Sink = config.SinkRef{URL: endpoint + "/sync"}
	cfg.Security.SharedSecret = compatSecret
	if err := push(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	got, err := sidecar.ReadSidecar(filepath.Join(h.Home, ".agentcookie", "cookies-plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "synthetic" {
		t.Fatalf("old v2 sink lost fixture: %#v", got)
	}
}
func TestPreviousCDPSourceSendsToCurrentSink(t *testing.T) {
	binary := oldBinary(t)
	h := New(t)
	browser := h.Browser()
	if err := livecdp.Inject(browser, []chrome.Cookie{{HostKey: "fixture.localhost", Name: "compat", Value: "synthetic", Path: "/", SameSite: -1}}); err != nil {
		t.Fatal(err)
	}
	applied := make(chan []chrome.Cookie, 1)
	handler, _, restore, err := cli.HermeticPipelineForTesting(filepath.Join(h.Home, "config"), compatSecret, func(chrome.Browser) (string, error) { t.Fatal("CDP source requested credentials"); return "", nil }, func(_ context.Context, cookies []chrome.Cookie) error { applied <- cookies; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	sink := httptest.NewServer(handler)
	t.Cleanup(sink.Close)
	dir := filepath.Join(h.Home, "old-source")
	writeCompatConfig(t, dir, "source.yaml", fmt.Sprintf("sink:\n  url: %s/sync\nsecurity:\n  shared_secret: %s\ncdp_source:\n  enabled: true\n  endpoint: %s\n", sink.URL, compatSecret, h.Endpoint))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--config-dir", dir, "source", "--once", "--skip-secrets", "--json")
	cmd.Env = fixtureEnvironment(h)
	cmd.Dir = h.Home
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("old CDP source: %v: %s", err, output)
	}
	select {
	case got := <-applied:
		if len(got) != 1 || got[0].Value != "synthetic" {
			t.Fatalf("old source lost fixture: %#v", got)
		}
	default:
		t.Fatal("old source did not reach new sink")
	}
}
