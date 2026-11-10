package storagecodec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/destination"
)

func loopbackEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "ws") {
		return errors.New("invalid local CDP endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("storage CDP endpoint must be loopback")
	}
	return nil
}

// NewAdapter composes cookie/lifecycle handling with storage. Each operation
// connects at the browser level, never creating an incidental visible target.
func NewAdapter(endpoint string, base destination.Applier) (*Adapter, error) {
	if err := loopbackEndpoint(endpoint); err != nil {
		return nil, err
	}
	if base == nil {
		return nil, errors.New("missing base applier")
	}
	return &Adapter{Base: base, Runner: &remoteRunner{endpoint: strings.TrimSuffix(endpoint, "/")}, Diagnostic: func(s string) { log.Print(s) }}, nil
}

type remoteRunner struct{ endpoint string }

func (r *remoteRunner) Run(context.Context, cdp.BrowserContextID, string, string, map[string]any, any) error {
	return errors.New("storage process incarnation required")
}
func (r *remoteRunner) browserURL(ctx context.Context, incarnation string) (string, error) {
	if incarnation == "" {
		return "", errors.New("storage process incarnation required")
	}
	endpoint := r.endpoint
	if !strings.HasPrefix(endpoint, "ws://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/version", nil)
		if err != nil {
			return "", err
		}
		client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("CDP discovery redirect refused") }}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("CDP discovery status %d", resp.StatusCode)
		}
		var v struct {
			URL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&v); err != nil {
			return "", err
		}
		endpoint = v.URL
	}
	if err := loopbackEndpoint(endpoint); err != nil {
		return "", err
	}
	if !strings.HasPrefix(endpoint, "ws://") {
		return "", errors.New("missing browser websocket")
	}
	if destination.Hash([]byte(endpoint)) != incarnation {
		return "", errors.New("chrome process changed during storage apply")
	}
	return endpoint, nil
}
func (r *remoteRunner) RunContext(ctx context.Context, c destination.Context, origin, operation string, args map[string]any, out any) error {
	endpoint, err := r.browserURL(ctx, c.Incarnation)
	if err != nil {
		return err
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	browser, err := chromedp.NewBrowser(life, endpoint)
	if err != nil {
		return err
	}
	// Attach the already-connected browser to a context without chromedp.Run:
	// Run on an unbound context would create a visible about:blank first window.
	root, closeRoot := chromedp.NewContext(life)
	defer closeRoot()
	chromedp.FromContext(root).Browser = browser
	return (&Helper{Browser: root}).Run(ctx, browserContext(c), origin, operation, args, out)
}
