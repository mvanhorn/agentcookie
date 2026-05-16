// Package chromemgr manages a dedicated Chrome subprocess that the sink uses
// as its cookie target. Chrome runs with --remote-debugging-port=0 (auto-pick)
// and an isolated --user-data-dir so the user's regular Chrome is untouched.
// The agentcookie sink talks to this Chrome via Chrome DevTools Protocol,
// avoiding the macOS Keychain prompt that direct SQLite writes require.
package chromemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultChromeBinary is the canonical Google Chrome path on macOS.
const DefaultChromeBinary = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

// devToolsActivePortFile is Chrome's two-line file written into the
// --user-data-dir when --remote-debugging-port is set. Line 1 is the port,
// line 2 is the browser-level WebSocket path.
const devToolsActivePortFile = "DevToolsActivePort"

// Config captures the inputs to a Manager.
type Config struct {
	// ChromeBinary is the absolute path to Chrome. Defaults to DefaultChromeBinary.
	ChromeBinary string
	// ProfileDir is the --user-data-dir Chrome will use. Required.
	ProfileDir string
	// UserAgent, if set, overrides Chrome's default User-Agent string. Used
	// for stealth (matching the source machine's UA).
	UserAgent string
	// ExtraArgs appended after the mandatory base args.
	ExtraArgs []string
	// StartupTimeout caps how long Start waits for DevToolsActivePort to appear.
	StartupTimeout time.Duration
}

func (c Config) chromeBinary() string {
	if c.ChromeBinary != "" {
		return c.ChromeBinary
	}
	return DefaultChromeBinary
}

func (c Config) startupTimeout() time.Duration {
	if c.StartupTimeout > 0 {
		return c.StartupTimeout
	}
	return 10 * time.Second
}

// Manager spawns and monitors a Chrome subprocess. Safe for concurrent use.
type Manager struct {
	cfg Config

	mu        sync.RWMutex
	cmd       *exec.Cmd
	port      int
	wsPath    string
	running   bool
	startTime time.Time

	supervisorCtx    context.Context
	supervisorCancel context.CancelFunc
	supervisorDone   chan struct{}
}

// New returns an unstarted Manager.
func New(cfg Config) (*Manager, error) {
	if cfg.ProfileDir == "" {
		return nil, errors.New("chromemgr: ProfileDir is required")
	}
	if _, err := os.Stat(cfg.chromeBinary()); err != nil {
		return nil, fmt.Errorf("chromemgr: Chrome binary not found at %s: %w", cfg.chromeBinary(), err)
	}
	if err := os.MkdirAll(cfg.ProfileDir, 0o755); err != nil {
		return nil, fmt.Errorf("chromemgr: create profile dir: %w", err)
	}
	return &Manager{cfg: cfg}, nil
}

// Start launches Chrome and waits up to StartupTimeout for the
// DevToolsActivePort file to appear. The supervisor goroutine that handles
// restart-on-crash is launched here and stops when Stop is called.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.supervisorCtx, m.supervisorCancel = context.WithCancel(context.Background())
	m.supervisorDone = make(chan struct{})
	m.mu.Unlock()

	if err := m.spawnOnce(ctx); err != nil {
		m.mu.Lock()
		m.supervisorCancel = nil
		m.mu.Unlock()
		return err
	}

	go m.supervisor()
	return nil
}

// spawnOnce launches a Chrome process and waits for DevToolsActivePort.
func (m *Manager) spawnOnce(ctx context.Context) error {
	// Remove stale DevToolsActivePort so we can wait for the fresh one.
	_ = os.Remove(filepath.Join(m.cfg.ProfileDir, devToolsActivePortFile))

	args := []string{
		"--remote-debugging-port=0",
		"--user-data-dir=" + m.cfg.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--no-startup-window",
		// --disable-blink-features=AutomationControlled removes the
		// navigator.webdriver=true marker that Cloudflare and other anti-bot
		// services check for.
		"--disable-blink-features=AutomationControlled",
	}
	if m.cfg.UserAgent != "" {
		args = append(args, "--user-agent="+m.cfg.UserAgent)
	}
	args = append(args, m.cfg.ExtraArgs...)

	cmd := exec.Command(m.cfg.chromeBinary(), args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	// Detach from controlling terminal so SIGINT to agentcookie does not
	// cascade to Chrome before our graceful shutdown runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Chrome: %w", err)
	}

	port, wsPath, err := waitForDevToolsPort(ctx, m.cfg.ProfileDir, m.cfg.startupTimeout())
	if err != nil {
		_ = killProcess(cmd)
		return fmt.Errorf("Chrome did not publish DevToolsActivePort: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.port = port
	m.wsPath = wsPath
	m.running = true
	m.startTime = time.Now()
	m.mu.Unlock()

	return nil
}

// supervisor runs in its own goroutine. Watches for Chrome process exit and
// restarts with exponential backoff. Exits when supervisorCtx is canceled.
func (m *Manager) supervisor() {
	defer close(m.supervisorDone)

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		m.mu.RLock()
		cmd := m.cmd
		ctx := m.supervisorCtx
		m.mu.RUnlock()
		if cmd == nil || ctx == nil {
			return
		}

		// Block until Chrome exits or supervisor ctx is canceled.
		exitCh := make(chan error, 1)
		go func() { exitCh <- cmd.Wait() }()

		select {
		case <-ctx.Done():
			// Stop() will kill the process and we return.
			return
		case <-exitCh:
			// Chrome exited; decide whether to restart.
		}

		m.mu.Lock()
		m.running = false
		m.cmd = nil
		m.port = 0
		m.wsPath = ""
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		if err := m.spawnOnce(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "chromemgr: restart failed: %v\n", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Second
	}
}

// Stop tears down Chrome and the supervisor. Graceful: SIGTERM first, then
// SIGKILL after 10 seconds if Chrome hasn't exited.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cancel := m.supervisorCancel
	cmd := m.cmd
	done := m.supervisorDone
	m.supervisorCancel = nil
	m.cmd = nil
	m.running = false
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		// Give Chrome 10 seconds to wind down.
		killed := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(killed)
		}()
		select {
		case <-killed:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	}
	if done != nil {
		<-done
	}
	return nil
}

// DebuggerURL returns the browser-level WebSocket URL for the running Chrome.
// Returns an error if Chrome is not currently running.
func (m *Manager) DebuggerURL() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.running {
		return "", errors.New("chromemgr: Chrome is not running")
	}
	return fmt.Sprintf("ws://127.0.0.1:%d%s", m.port, m.wsPath), nil
}

// IsRunning reports whether Chrome is currently up.
func (m *Manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// waitForDevToolsPort polls for the DevToolsActivePort file under profileDir.
// Returns the port and the WebSocket path on success.
func waitForDevToolsPort(ctx context.Context, profileDir string, timeout time.Duration) (int, string, error) {
	path := filepath.Join(profileDir, devToolsActivePortFile)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		default:
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			port, wsPath, parseErr := parseDevToolsActivePort(data)
			if parseErr == nil {
				return port, wsPath, nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return 0, "", fmt.Errorf("timed out waiting for %s after %s", path, timeout)
}

func parseDevToolsActivePort(data []byte) (int, string, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		return 0, "", errors.New("empty file")
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", fmt.Errorf("parse port: %w", err)
	}
	wsPath := ""
	if len(lines) >= 2 {
		wsPath = strings.TrimSpace(lines[1])
	}
	return port, wsPath, nil
}

func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return cmd.Process.Kill()
	}
	return nil
}
