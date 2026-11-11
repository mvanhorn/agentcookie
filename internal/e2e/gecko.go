package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/gecko/bidi"
	"github.com/mvanhorn/agentcookie/internal/gecko/broker"
	"github.com/mvanhorn/agentcookie/internal/gecko/companion"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
)

// GeckoFixture owns every process/profile it creates. Browser gates are opt-in
// and fail closed when CI requests them without the required binaries.
type GeckoFixture struct {
	Home, Profile, StateDir, Endpoint, Binary, AgentcookieBinary string
	BiDiPort, CompanionPort                                      int
	t                                                            *testing.T
	mu                                                           sync.Mutex
	cmd                                                          *exec.Cmd
	keepHome                                                     bool
	log                                                          *os.File
}

func fixturePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
func NewGecko(t *testing.T) *GeckoFixture {
	t.Helper()
	binary, agent := os.Getenv("AGENTCOOKIE_GECKO_BINARY"), os.Getenv("AGENTCOOKIE_BINARY")
	if binary == "" || agent == "" {
		if os.Getenv("AGENTCOOKIE_GECKO_REQUIRED") == "1" {
			t.Fatal("Gecko gate requires AGENTCOOKIE_GECKO_BINARY and AGENTCOOKIE_BINARY")
		}
		t.Skip("Gecko lifecycle gate not requested; explicit fixture binaries required")
	}
	for _, path := range []string{binary, agent} {
		st, e := os.Stat(path)
		if e != nil || st.IsDir() {
			t.Fatalf("fixture binary unavailable: %s (%v)", path, e)
		}
	}
	h := New(t)
	// Unix socket paths are limited to 104 bytes on macOS. Use a short private
	// throwaway HOME rather than a long Go subtest directory, never a real profile.
	short, e := os.MkdirTemp("/tmp", "acg-")
	if e != nil {
		t.Fatal(e)
	}
	h.Home = short
	t.Setenv("HOME", short)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(short, "config"))
	t.Setenv("TMPDIR", short)
	g := &GeckoFixture{Home: h.Home, Profile: filepath.Join(h.Home, "gecko-profile"), StateDir: filepath.Join(h.Home, "gecko-state"), Binary: binary, AgentcookieBinary: agent, t: t}
	t.Cleanup(func() {
		g.mu.Lock()
		keep := g.keepHome
		g.mu.Unlock()
		if keep {
			t.Logf("retaining fixture home after incomplete process cleanup: %s", short)
			return
		}
		_ = os.RemoveAll(short)
	})
	g.BiDiPort = fixturePort(t)
	g.CompanionPort = fixturePort(t)
	for g.CompanionPort == g.BiDiPort {
		g.CompanionPort = fixturePort(t)
	}
	if e := os.MkdirAll(g.Profile, 0700); e != nil {
		t.Fatal(e)
	}
	if e := writeGeckoFixturePreferences(g.Profile); e != nil {
		t.Fatal(e)
	}
	if real, e := filepath.EvalSymlinks(g.Profile); e == nil {
		g.Profile = real
	}
	g.Endpoint = fmt.Sprintf("ws://127.0.0.1:%d/session", g.BiDiPort)
	t.Cleanup(func() { g.StopBroker(); g.StopBrowser() })
	g.RestartBrowser()
	return g
}
func (g *GeckoFixture) ReaderOptions() geckoreader.Options {
	return geckoreader.Options{Name: "firefox", Profile: g.Profile, Mode: "live", StateDir: g.StateDir, BiDiPort: g.BiDiPort, CompanionPort: g.CompanionPort, Executable: g.AgentcookieBinary}
}
func (g *GeckoFixture) BrokerOptions() broker.Options {
	return broker.Options{Profile: g.Profile, Endpoint: g.Endpoint, StateDir: g.StateDir, CompanionPort: g.CompanionPort, Executable: g.AgentcookieBinary}
}
func (g *GeckoFixture) RestartBrowser() {
	g.t.Helper()
	g.StopBrowser()
	g.mu.Lock()
	incomplete := g.cmd != nil
	g.mu.Unlock()
	if incomplete {
		g.t.Fatal("cannot restart fixture browser before previous owned group cleanup completes")
	}
	args := []string{"-no-remote", "-profile", g.Profile, fmt.Sprintf("--remote-debugging-port=%d", g.BiDiPort), "about:blank"}
	if os.Getenv("AGENTCOOKIE_GECKO_HEADED") != "1" {
		args = append([]string{"-headless"}, args...)
	}
	cmd := exec.Command(g.Binary, args...)
	cmd.Env = geckoFixtureEnvironment()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	log, e := os.OpenFile(filepath.Join(g.Home, "gecko-browser.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		g.t.Fatal(e)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		g.t.Fatal(e)
	}
	// Do not call Wait until the complete process group has been stopped.
	// An exited, unreaped child reserves its PID, preventing a reused group ID
	// from being signaled if the browser parent exits before its descendants.
	g.mu.Lock()
	g.cmd = cmd
	g.log = log
	g.mu.Unlock()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", g.BiDiPort), 100*time.Millisecond)
		if e == nil {
			conn.Close()
			if _, e = os.Stat(filepath.Join(g.Profile, "WebDriverBiDiServer.json")); e == nil {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	g.t.Fatal("fixture Gecko did not expose its explicit profile BiDi endpoint")
}
func (g *GeckoFixture) StopBrowser() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd == nil {
		return
	}
	if err := stopOwnedFixtureGroup(g.cmd, 5*time.Second); err != nil {
		g.keepHome = true
		g.t.Errorf("fixture browser group %d cleanup failed: %v", g.cmd.Process.Pid, err)
		return
	}
	g.cmd = nil
	if g.log != nil {
		_ = g.log.Close()
		g.log = nil
	}
}
func (g *GeckoFixture) StopBroker() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if b, err := broker.Connect(g.BrokerOptions()); err == nil {
		_ = b.End(ctx)
		_ = b.Close()
	}
	cancel()
	// Run holds this lock before opening BiDi and until every deferred resource
	// cleanup has finished. It also covers a broker still in its startup phase.
	lockPath := filepath.Join(g.StateDir, "gecko-broker", companion.ProfileKey(g.Profile)+".json.lock")
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err := waitFixtureBrokerStopped(ctx, lockPath)
	cancel()
	if err == nil {
		return
	}
	// The broker owns a detached session. Never signal the PID in its state:
	// that PID can have been reused. Closing our browser tears down its BiDi
	// connection, which the broker observes even when authenticated End failed.
	g.StopBrowser()
	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	err = waitFixtureBrokerStopped(ctx, lockPath)
	cancel()
	if err != nil {
		g.mu.Lock()
		g.keepHome = true
		g.mu.Unlock()
		g.t.Errorf("fixture broker cleanup unverified: %v; retained state directory %s, profile %s, endpoint %s (no broker PID signaled)", err, g.StateDir, g.Profile, g.Endpoint)
	}
}
func (g *GeckoFixture) BrowserProcessID() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cmd == nil {
		return 0
	}
	return g.cmd.Process.Pid
}
func (g *GeckoFixture) BrokerState() broker.State {
	g.t.Helper()
	raw, e := os.ReadFile(filepath.Join(g.StateDir, "gecko-broker", companion.ProfileKey(g.Profile)+".json"))
	if e != nil {
		g.t.Fatal(e)
	}
	var state broker.State
	if e = json.Unmarshal(raw, &state); e != nil {
		g.t.Fatal(e)
	}
	return state
}

// InstallFixtureExtension is only for fixture setup before the broker starts.
// The temporary extension stays installed after this owned BiDi session ends.
func (g *GeckoFixture) InstallFixtureExtension(script string) {
	g.t.Helper()
	dir, e := os.MkdirTemp(g.Home, "fixture-extension-")
	if e != nil {
		g.t.Fatal(e)
	}
	manifest := map[string]any{"manifest_version": 2, "name": "agentcookie hermetic fixture", "version": "1.0", "browser_specific_settings": map[string]any{"gecko": map[string]string{"id": "agentcookie-fixture@agentcookie.dev"}}, "permissions": []string{"cookies", "contextualIdentities", "tabs", "webNavigation", "<all_urls>"}, "background": map[string]any{"scripts": []string{"background.js"}}}
	raw, _ := json.Marshal(manifest)
	if e = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); e != nil {
		g.t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "background.js"), []byte(script), 0600); e != nil {
		g.t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, e := bidi.Dial(ctx, g.Endpoint)
	if e != nil {
		g.t.Fatal(e)
	}
	defer client.Close()
	if e = client.NewSession(ctx, g.Profile); e != nil {
		g.t.Fatal(e)
	}
	defer func() { _ = client.End(ctx) }()
	if e = client.Install(ctx, dir); e != nil {
		g.t.Fatal(e)
	}
}
