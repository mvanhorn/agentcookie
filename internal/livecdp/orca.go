package livecdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// DefaultOrcaCDP is the loopback CDP endpoint orca exposes when launched with
// --remote-debugging-port=9222. orca is Electron, so it speaks the same
// DevTools Protocol this package already uses for cmux's Chromium sibling and
// the agent browsers; orca-sync points the injector at orca's own webview.
const DefaultOrcaCDP = "http://127.0.0.1:9222"

// orcaAttachTimeout bounds a single attach+inject against one orca webview.
const orcaAttachTimeout = 30 * time.Second

// cdpTarget is the subset of a CDP /json/list entry orca-sync needs.
type cdpTarget struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

// FindOrcaWebviewTargets returns the target IDs of orca's open browser panes.
// orca renders each browser pane as a CDP "webview" target on the
// persist:orca-browser partition; orca-sync attaches to those. It deliberately
// does NOT create a target: Electron rejects Target.createTarget with
// "-32000 Not supported", so the only way in is to attach to a pane the user
// already has open.
func FindOrcaWebviewTargets(base string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/json/list", nil)
	if err != nil {
		return nil, fmt.Errorf("orca cdp: build request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("orca cdp: GET /json/list (%s) -- is orca running with --remote-debugging-port? %w", base, err)
	}
	defer resp.Body.Close()

	var targets []cdpTarget
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return nil, fmt.Errorf("orca cdp: decode /json/list: %w", err)
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.Type == "webview" {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

// InjectIntoOrca injects cookies into every open orca browser pane reachable
// at base, and returns the number of panes injected. A failure on one pane
// (e.g. it closed mid-sync) does not abort the others. Zero open panes is not
// an error -- it returns (0, nil) and the caller decides how to report "open a
// browser pane in orca". An empty cookie slice is a no-op.
//
// Injection lands in the persist:orca-browser partition, so a single open pane
// authenticates the partition for the panes the user opens next.
func InjectIntoOrca(base string, cookies []chrome.Cookie) (int, error) {
	if len(cookies) == 0 {
		return 0, nil
	}
	ids, err := FindOrcaWebviewTargets(base)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, id := range ids {
		if err := injectIntoOrcaTarget(base, id, cookies); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// injectIntoOrcaTarget attaches to one existing orca webview target and sets
// the cookies on it via Network.setCookies (the proven shaping in Inject).
func injectIntoOrcaTarget(base, id string, cookies []chrome.Cookie) error {
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), base)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithTargetID(target.ID(id)))
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, orcaAttachTimeout)
	defer tcancel()

	if err := chromedp.Run(ctx); err != nil {
		return fmt.Errorf("attach to orca webview %s: %w", id, err)
	}
	return Inject(ctx, cookies)
}
