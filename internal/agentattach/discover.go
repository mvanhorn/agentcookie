package agentattach

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultDebugPort is the conventional Chrome remote-debugging port and
// the broker's default probe target.
const DefaultDebugPort = 9222

// probeTimeout caps the /json/version probe. The endpoint is loopback,
// so a live Chrome answers in milliseconds; a short timeout keeps an
// unreachable port from stalling the broker.
const probeTimeout = 2 * time.Second

// Discovery is the result of probing a Chrome CDP endpoint: whether it is
// reachable, the browser-level WebSocket to attach to, the detected major
// version and its policy tier, and -- when the endpoint is not directly
// usable -- a one-line remediation naming the next step.
type Discovery struct {
	Reachable   bool
	WSEndpoint  string
	Version     int
	Tier        PolicyTier
	Remediation string
}

// versionResponse is the subset of Chrome's /json/version payload we use.
type versionResponse struct {
	Browser              string `json:"Browser"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Discover probes the loopback CDP endpoint on the given port and returns
// a Discovery describing how (or whether) the user's Chrome can be
// attached. It never reads cookies and never mutates anything. When the
// endpoint is unreachable it falls back to the installed Chrome version
// to choose the right remediation (enable autoConnect vs. use the
// debug-profile fallback).
func Discover(ctx context.Context, port int) Discovery {
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	return discoverAt(ctx, baseURL, port, installedChromeMajor)
}

// discoverAt is the testable core: baseURL is the CDP host root, port is
// reported back in remediations, and installedMajor supplies the
// installed Chrome version when the live endpoint is unreachable.
func discoverAt(ctx context.Context, baseURL string, port int, installedMajor func() int) Discovery {
	ver, err := fetchVersion(ctx, baseURL)
	if err != nil {
		// Endpoint down: pick the remediation from the installed version.
		major := installedMajor()
		tier := TierForVersion(major)
		d := Discovery{Reachable: false, Version: major, Tier: tier}
		d.Remediation = remediationFor(d, port)
		return d
	}

	major, _ := ParseChromeMajor(ver.Browser)
	d := Discovery{
		Reachable:  true,
		WSEndpoint: ver.WebSocketDebuggerURL,
		Version:    major,
		Tier:       TierForVersion(major),
	}
	if d.WSEndpoint == "" {
		// Reachable but no browser-level socket advertised: unusable for a
		// browser-level attach, surface it rather than returning a hollow OK.
		d.Remediation = fmt.Sprintf("Chrome answered on port %d but advertised no browser WebSocket; restart Chrome's debugging session and retry.", port)
	}
	return d
}

// fetchVersion GETs /json/version and decodes the fields we use.
func fetchVersion(ctx context.Context, baseURL string) (versionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/json/version", nil)
	if err != nil {
		return versionResponse{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return versionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return versionResponse{}, fmt.Errorf("agentattach: /json/version returned %d", resp.StatusCode)
	}
	var v versionResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return versionResponse{}, fmt.Errorf("agentattach: decode /json/version: %w", err)
	}
	return v, nil
}

// remediationFor returns the one-line next step for a Discovery whose
// endpoint is unreachable, keyed on the policy tier.
func remediationFor(d Discovery, port int) string {
	switch d.Tier {
	case TierAutoConnect:
		return fmt.Sprintf("Chrome %d supports attaching your real profile. Enable it once: open chrome://inspect#remote-debugging, turn the toggle on, then re-run `agentcookie attach`. (Looked for the debug endpoint on 127.0.0.1:%d.)", d.Version, port)
	case TierCustomDirOnly:
		return fmt.Sprintf("Chrome %d cannot expose remote debugging on your real profile (the Chrome 136+ restriction). Use the synced debug profile instead: `agentcookie attach --fallback`.", d.Version)
	case TierLegacy:
		return fmt.Sprintf("Start Chrome with --remote-debugging-port=%d, or use the synced debug profile: `agentcookie attach --fallback`.", port)
	default:
		return fmt.Sprintf("Could not reach a Chrome debug endpoint on 127.0.0.1:%d and could not detect your Chrome version. Use the synced debug profile: `agentcookie attach --fallback`.", port)
	}
}
