package livecdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

type ownedReceipt struct {
	PID       int    `json:"pid"`
	Profile   string `json:"profile"`
	Endpoint  string `json:"endpoint"`
	WebSocket string `json:"websocket"`
}

func ownedReceiptPath(profile string) string {
	return filepath.Join(profile, ".agentcookie-owned.json")
}
func browserWebSocket(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/json/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("CDP discovery returned %d", resp.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v)
	if err == nil && v.URL == "" {
		err = errors.New("browser websocket URL missing")
	}
	if err == nil {
		u, _ := url.Parse(endpoint)
		ws, parseErr := url.Parse(v.URL)
		if parseErr != nil || ws.Scheme != "ws" || ws.User != nil || ws.Host != u.Host || !strings.HasPrefix(ws.Path, "/devtools/browser/") {
			return "", errors.New("CDP discovery endpoint mismatch")
		}
	}
	return v.URL, err
}
func (o *OwnedChrome) saveReceipt(ctx context.Context) error {
	ws, err := browserWebSocket(ctx, o.Endpoint)
	if err != nil {
		return err
	}
	r := ownedReceipt{PID: o.cmd.Process.Pid, Profile: o.UserDataDir, Endpoint: o.Endpoint, WebSocket: ws}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := ownedReceiptPath(o.UserDataDir)
	f, err := os.CreateTemp(o.UserDataDir, ".owner-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(o.UserDataDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ResumeOwnedChrome only adopts a surviving process when its private receipt,
// exact profile, PID and browser UUID all agree. An arbitrary occupied port is
// never sufficient. A missing/stale receipt permits a fresh launch only when
// the recorded process is known to be gone.
func ResumeOwnedChrome(ctx context.Context, profile string, port int) (*OwnedChrome, error) {
	data, err := os.ReadFile(ownedReceiptPath(profile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r ownedReceipt
	if err = json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("owned browser receipt corrupt: %w", err)
	}
	abs, err := filepath.Abs(profile)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if r.Profile != abs || r.Endpoint != endpoint || r.PID <= 0 {
		return nil, fmt.Errorf("owned browser receipt identity mismatch")
	}
	current, err := browserWebSocket(ctx, endpoint)
	if err != nil {
		process, findErr := os.FindProcess(r.PID)
		if findErr == nil && errors.Is(process.Signal(syscall.Signal(0)), os.ErrProcessDone) {
			_ = os.Remove(ownedReceiptPath(profile))
			return nil, nil
		}
		if findErr == nil && errors.Is(process.Signal(syscall.Signal(0)), syscall.ESRCH) {
			_ = os.Remove(ownedReceiptPath(profile))
			return nil, nil
		}
		return nil, fmt.Errorf("owned Chrome receipt exists but process cannot be verified: %w", err)
	}
	if current != r.WebSocket {
		return nil, fmt.Errorf("occupied CDP port does not match owned browser incarnation")
	}
	if err = verifyOwnedEndpoint(ctx, endpoint, profile, r.PID); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	o := &OwnedChrome{Port: port, Endpoint: endpoint, UserDataDir: profile, done: make(chan struct{}), adopted: true, receipt: r, monitorCancel: cancel}
	go func() {
		defer close(o.done)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-tick.C:
				probe, c := context.WithTimeout(life, 3*time.Second)
				ws, err := browserWebSocket(probe, endpoint)
				c()
				if err != nil || ws != r.WebSocket {
					return
				}
			}
		}
	}()
	return o, nil
}
func (o *OwnedChrome) closeAdopted() error {
	defer func() {
		if o.monitorCancel != nil {
			o.monitorCancel()
		}
		if o.done != nil {
			<-o.done
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := verifyOwnedEndpoint(ctx, o.Endpoint, o.UserDataDir, o.receipt.PID); err != nil {
		return err
	}
	b, err := chromedp.NewBrowser(ctx, o.receipt.WebSocket)
	if err != nil {
		return err
	}
	err = browser.Close().Do(cdp.WithExecutor(ctx, b))
	if err == nil {
		_ = os.Remove(ownedReceiptPath(o.UserDataDir))
	}
	return err
}

// VerifyOwnedChrome proves current receipt, process, profile and browser UUID
// without adopting, launching, closing, removing receipts or starting monitors.
func VerifyOwnedChrome(ctx context.Context, profile string, port int) error {
	_, err := VerifiedOwnedChromeWebSocket(ctx, profile, port)
	return err
}

// VerifiedOwnedChromeWebSocket returns the proven process-specific endpoint.
// Read-only probes must connect to this exact websocket, not rediscover a port
// after verification, which could have been reused by another process.
func VerifiedOwnedChromeWebSocket(ctx context.Context, profile string, port int) (string, error) {
	path := ownedReceiptPath(profile)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return "", fmt.Errorf("invalid owned browser receipt file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var r ownedReceipt
	if err = json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("owned browser receipt corrupt: %w", err)
	}
	abs, err := filepath.Abs(profile)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if r.Profile != abs || r.Endpoint != endpoint || r.PID <= 0 {
		return "", fmt.Errorf("owned browser receipt identity mismatch")
	}
	current, err := browserWebSocket(ctx, endpoint)
	if err != nil {
		return "", err
	}
	if current != r.WebSocket {
		return "", fmt.Errorf("occupied CDP port does not match owned browser incarnation")
	}
	if err = verifyOwnedEndpoint(ctx, endpoint, profile, r.PID); err != nil {
		return "", err
	}
	return current, nil
}
