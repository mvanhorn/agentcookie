// Package chromeconn discovers and attaches to the user's running Chrome via
// the chrome://inspect/#remote-debugging activation surface (Chrome 144+).
//
// The discovery contract is documented in
// docs/research/chrome-144-remote-debugging.md. In short: Chrome writes a
// two-line DevToolsActivePort file under the user-data-dir root when remote
// debugging is enabled, containing the dynamic port and the browser-level
// WebSocket path. Clients connect directly via ws://127.0.0.1:<port><path>.
// The legacy HTTP /json/version discovery is unreliable on activations
// driven by the chrome://inspect toggle and is not used here.
package chromeconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// devToolsActivePortFile is the filename Chrome writes inside the
// user-data-dir root when remote debugging is active.
const devToolsActivePortFile = "DevToolsActivePort"

// ErrRemoteDebuggingNotEnabled is returned when the DevToolsActivePort file is
// missing from the user-data-dir. The wizard surfaces this as a one-step user
// instruction (open chrome://inspect/#remote-debugging and toggle Allow).
var ErrRemoteDebuggingNotEnabled = errors.New("chromeconn: Chrome remote debugging is not enabled (DevToolsActivePort missing); open chrome://inspect/#remote-debugging and enable remote debugging")

// ErrStaleEndpoint is returned when DevToolsActivePort exists but the
// advertised port no longer accepts WebSocket connections. Typically means
// Chrome was force-killed and a stale file was left behind.
var ErrStaleEndpoint = errors.New("chromeconn: DevToolsActivePort is stale; Chrome may have crashed since the file was written")

// Endpoint identifies a Chrome DevTools Protocol WebSocket. Port is the
// dynamic port Chrome bound, WSPath is the browser-level WebSocket path.
type Endpoint struct {
	Port   int
	WSPath string
}

// WebSocketURL returns the loopback ws:// URL chromedp connects to.
func (e Endpoint) WebSocketURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d%s", e.Port, e.WSPath)
}

// DefaultProfileDir returns the macOS default Chrome user-data-dir. The
// DevToolsActivePort file, when present, lives at this directory's root.
func DefaultProfileDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
}

// DiscoverDefault is shorthand for Discover(DefaultProfileDir()).
func DiscoverDefault() (Endpoint, error) {
	return Discover(DefaultProfileDir())
}

// Discover reads DevToolsActivePort from the given user-data-dir and parses
// the two-line file. Returns ErrRemoteDebuggingNotEnabled when the file is
// missing.
func Discover(userDataDir string) (Endpoint, error) {
	if userDataDir == "" {
		return Endpoint{}, errors.New("chromeconn: empty user-data-dir")
	}
	path := filepath.Join(userDataDir, devToolsActivePortFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Endpoint{}, ErrRemoteDebuggingNotEnabled
		}
		return Endpoint{}, fmt.Errorf("chromeconn: read %s: %w", path, err)
	}
	return parseDevToolsActivePort(data)
}

// parseDevToolsActivePort accepts the raw file bytes and returns an Endpoint.
// Line 1 must be a base-10 port (1..65535). Line 2 must be a non-empty path
// beginning with "/".
func parseDevToolsActivePort(data []byte) (Endpoint, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return Endpoint{}, fmt.Errorf("chromeconn: DevToolsActivePort needs 2 lines, got %d", len(lines))
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return Endpoint{}, fmt.Errorf("chromeconn: parse port: %w", err)
	}
	if port <= 0 || port > 65535 {
		return Endpoint{}, fmt.Errorf("chromeconn: port %d out of range", port)
	}
	wsPath := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(wsPath, "/") {
		return Endpoint{}, fmt.Errorf("chromeconn: ws path %q must begin with /", wsPath)
	}
	return Endpoint{Port: port, WSPath: wsPath}, nil
}

// ProbeReachable opens a TCP connection to the endpoint port and closes it.
// Returns ErrStaleEndpoint when the port no longer accepts connections.
// Cheap pre-check before spinning up a chromedp allocator.
func ProbeReachable(ctx context.Context, ep Endpoint) error {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", ep.Port))
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return fmt.Errorf("%w: %v", ErrStaleEndpoint, err)
		}
		return fmt.Errorf("chromeconn: probe %d: %w", ep.Port, err)
	}
	_ = conn.Close()
	return nil
}

// Attach returns a chromedp context attached to the discovered Chrome via the
// browser-level WebSocket. The returned cancel func tears down both the
// allocator and the per-attachment context. The caller owns lifecycle.
//
// chromedp's NewRemoteAllocator expects an HTTP-style URL by default; with
// chromedp.NoModifyURL it accepts a ws:// URL directly, which is what
// chrome://inspect-driven activations need (the HTTP discovery surface is
// unreliable on those activations).
func Attach(parent context.Context, ep Endpoint) (context.Context, context.CancelFunc, error) {
	wsURL := ep.WebSocketURL()
	if _, err := url.Parse(wsURL); err != nil {
		return nil, nil, fmt.Errorf("chromeconn: invalid ws URL %q: %w", wsURL, err)
	}
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(parent, wsURL, chromedp.NoModifyURL)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	cancel := func() {
		cancelCtx()
		cancelAlloc()
	}
	return ctx, cancel, nil
}

// AttachWithDeadline is a helper for callers that want a bounded handshake.
// If the chromedp context is not usable within timeout, cancels and returns
// the error. Useful for sink startup and /healthz handlers.
func AttachWithDeadline(parent context.Context, ep Endpoint, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	ctx, cancel, err := Attach(parent, ep)
	if err != nil {
		return nil, nil, err
	}
	handshake, hcancel := context.WithTimeout(ctx, timeout)
	defer hcancel()
	if err := chromedp.Run(handshake); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("chromeconn: chromedp handshake: %w", err)
	}
	return ctx, cancel, nil
}
