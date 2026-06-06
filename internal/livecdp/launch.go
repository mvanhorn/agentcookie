package livecdp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// FindChrome locates the Google Chrome (or Chromium) executable. macOS app
// bundles first (the agentcookie target platform), then PATH. The owned
// browser must be REAL Chrome -- it shares the user's "Chrome Safe Storage"
// Keychain item only within the same install, and matching the source
// browser keeps cookie semantics consistent.
func FindChrome() (string, error) {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"))
	}
	candidates = append(candidates,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	)
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find Google Chrome; install it or pass --chrome-path")
}

// OwnedChrome is a Chrome instance agentcookie launched and owns. It runs on
// a DEDICATED user-data-dir (so --remote-debugging-port is honored -- Chrome
// 136+ only blocks the flag on the default profile dir) and a loopback debug
// port, leaving the user's everyday Chrome untouched (no single-instance lock).
type OwnedChrome struct {
	cmd         *exec.Cmd
	Port        int
	Endpoint    string // http://127.0.0.1:<port>
	UserDataDir string
}

// LaunchOwnedChrome starts Chrome on userDataDir with a loopback debug port,
// waits for the CDP endpoint, and returns the handle. chromePath empty ->
// FindChrome. headless uses the new headless mode (full feature parity with
// headed for cookie/context behavior).
func LaunchOwnedChrome(ctx context.Context, chromePath, userDataDir string, port int, headless bool) (*OwnedChrome, error) {
	if chromePath == "" {
		var err error
		if chromePath, err = FindChrome(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(userDataDir, 0o755); err != nil {
		return nil, fmt.Errorf("livecdp: create user-data-dir %q: %w", userDataDir, err)
	}
	args := []string{
		"--user-data-dir=" + userDataDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")

	cmd := exec.Command(chromePath, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("livecdp: launch chrome: %w", err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForCDP(ctx, endpoint, 25*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}
	return &OwnedChrome{cmd: cmd, Port: port, Endpoint: endpoint, UserDataDir: userDataDir}, nil
}

// Close shuts down the owned Chrome: SIGTERM, then SIGKILL if it lingers.
func (o *OwnedChrome) Close() error {
	if o == nil || o.cmd == nil || o.cmd.Process == nil {
		return nil
	}
	_ = o.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = o.cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = o.cmd.Process.Kill()
	}
	return nil
}

// waitForCDP polls the CDP /json/version endpoint until it responds 200 or
// the timeout/ctx elapses.
func waitForCDP(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(endpoint + "/json/version")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("livecdp: chrome CDP endpoint %s not reachable within %s", endpoint, timeout)
}
