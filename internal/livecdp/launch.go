package livecdp

import (
	"context"
	"fmt"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/systeminfo"
	"github.com/chromedp/chromedp"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	cmd           *exec.Cmd
	done          chan struct{}
	mu            sync.Mutex
	exitErr       error
	adopted       bool
	receipt       ownedReceipt
	monitorCancel context.CancelFunc
	Port          int
	Endpoint      string // http://127.0.0.1:<port>
	UserDataDir   string
}

// LaunchOwnedChrome starts Chrome on userDataDir with a loopback debug port,
// waits for the CDP endpoint, and returns the handle. chromePath empty ->
// FindChrome. headless uses the new headless mode (full feature parity with
// headed for cookie/context behavior).
func LaunchOwnedChrome(ctx context.Context, chromePath, userDataDir string, port int, headless bool) (*OwnedChrome, error) {
	return LaunchOwnedChromeOptions(ctx, chromePath, userDataDir, port, LaunchOptions{Headless: headless})
}

// LaunchOptions keeps fixture credentials explicit and prevents the initial
// visible window from opening before durable destination restore.
type LaunchOptions struct {
	Headless        bool
	NoStartupWindow bool
	MockCredentials bool
}

func LaunchOwnedChromeOptions(ctx context.Context, chromePath, userDataDir string, port int, opts LaunchOptions) (*OwnedChrome, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid debugging port")
	}
	probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("livecdp: debugging port occupied: %w", err)
	}
	_ = probe.Close()
	userDataDir, err = filepath.Abs(userDataDir)
	if err != nil {
		return nil, err
	}

	if chromePath == "" {
		var err error
		if chromePath, err = FindChrome(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(userDataDir, 0o700); err != nil {
		return nil, fmt.Errorf("livecdp: create user-data-dir %q: %w", userDataDir, err)
	}
	args := []string{
		"--user-data-dir=" + userDataDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-debugging-address=127.0.0.1",
		"--no-first-run",
		"--no-default-browser-check",
		"--enable-automation",
	}
	if opts.Headless {
		args = append(args, "--headless=new")
	}
	if opts.NoStartupWindow {
		args = append(args, "--no-startup-window", "--enable-automation")
	} else {
		args = append(args, "about:blank")
	}
	if opts.MockCredentials {
		args = append(args, "--use-mock-keychain", "--password-store=basic")
	}

	cmd := exec.Command(chromePath, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("livecdp: launch chrome: %w", err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	o := &OwnedChrome{cmd: cmd, Port: port, Endpoint: endpoint, UserDataDir: userDataDir, done: make(chan struct{})}
	go func() { err := cmd.Wait(); o.mu.Lock(); o.exitErr = err; o.mu.Unlock(); close(o.done) }()
	readyCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	wait := make(chan error, 1)
	go func() { wait <- waitForCDP(readyCtx, endpoint, 25*time.Second) }()
	select {
	case <-o.done:
		return nil, fmt.Errorf("livecdp: launched Chrome exited before CDP became ready")
	case err := <-wait:
		if err != nil {
			_ = o.Close()
			return nil, err
		}
	case <-readyCtx.Done():
		_ = o.Close()
		return nil, readyCtx.Err()
	}
	if err := verifyOwnedEndpoint(readyCtx, endpoint, userDataDir, cmd.Process.Pid); err != nil {
		_ = o.Close()
		return nil, err
	}
	if opts.NoStartupWindow {
		if err := o.saveReceipt(readyCtx); err != nil {
			_ = o.Close()
			return nil, err
		}
	}
	return o, nil
}

// Close shuts down the owned Chrome: SIGTERM, then SIGKILL if it lingers.
func (o *OwnedChrome) Close() error {
	if o != nil && o.adopted {
		return o.closeAdopted()
	}
	if o == nil || o.cmd == nil || o.cmd.Process == nil {
		return nil
	}
	_ = o.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-o.done:
	case <-time.After(5 * time.Second):
		_ = o.cmd.Process.Kill()
		<-o.done
	}
	_ = os.Remove(ownedReceiptPath(o.UserDataDir))
	return nil
}

// Exited distinguishes a normal browser shutdown from a process crash. A
// disconnected CDP socket alone is not evidence of a crash.
func (o *OwnedChrome) Exited() (exited, clean bool) {
	if o == nil {
		return false, false
	}
	select {
	case <-o.done:
		o.mu.Lock()
		defer o.mu.Unlock()
		return true, o.adopted || o.exitErr == nil
	default:
		return false, false
	}
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

// verifyOwnedEndpoint prevents an occupied/raced port from selecting an
// unrelated Chrome. Both the exact profile argument and browser PID must match
// the process just launched, before any cookie read or write is allowed.
func verifyOwnedEndpoint(ctx context.Context, endpoint, userDataDir string, pid int) error {
	websocket, err := browserWebSocket(ctx, endpoint)
	if err != nil {
		return err
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	b, err := chromedp.NewBrowser(life, websocket)
	if err != nil {
		return err
	}
	exec := cdp.WithExecutor(ctx, b)
	args, err := browser.GetBrowserCommandLine().Do(exec)
	if err != nil {
		return fmt.Errorf("verify owned Chrome arguments: %w", err)
	}
	profileOK := false
	for _, arg := range args {
		if after, ok := strings.CutPrefix(arg, "--user-data-dir="); ok {
			actual := after
			a, _ := filepath.EvalSymlinks(actual)
			wanted, _ := filepath.EvalSymlinks(userDataDir)
			profileOK = a != "" && a == wanted
		}
	}
	if !profileOK {
		return fmt.Errorf("CDP browser profile does not match owned profile")
	}
	processes, err := systeminfo.GetProcessInfo().Do(exec)
	if err != nil {
		return fmt.Errorf("verify owned Chrome PID: %w", err)
	}
	for _, process := range processes {
		if process.Type == "browser" && process.ID == int64(pid) {
			return nil
		}
	}
	return fmt.Errorf("CDP browser PID does not match launched process")
}
