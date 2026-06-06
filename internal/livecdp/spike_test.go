package livecdp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// TestSpikeInjectRunningChrome connects to an ALREADY-RUNNING Chrome (whose
// http endpoint is in AGENTCOOKIE_SPIKE_CDP, e.g. http://127.0.0.1:9400) and
// injects the cookies in AGENTCOOKIE_SPIKE_STATE (a Playwright storage_state
// JSON of plaintext cookies) into every context. Used to verify, against a
// real browser-use connected to the same Chrome, that injection reaches
// browser-use's own context. Not a normal unit test -- it drives an external
// process orchestrated by the spike shell flow.
func TestSpikeInjectRunningChrome(t *testing.T) {
	endpoint := os.Getenv("AGENTCOOKIE_SPIKE_CDP")
	statePath := os.Getenv("AGENTCOOKIE_SPIKE_STATE")
	if endpoint == "" || statePath == "" {
		t.Skip("set AGENTCOOKIE_SPIKE_CDP and AGENTCOOKIE_SPIKE_STATE to run the running-Chrome spike")
	}

	cookies := loadSpikeCookies(t, statePath)
	t.Logf("loaded %d cookies from %s", len(cookies), statePath)

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), endpoint)
	defer allocCancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	ctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
	defer tcancel()

	// Force the connection up.
	if err := chromedp.Run(ctx); err != nil {
		t.Fatalf("connect to running Chrome at %s: %v", endpoint, err)
	}

	n, err := InjectAllContexts(ctx, cookies)
	if err != nil {
		t.Fatalf("InjectAllContexts: %v", err)
	}
	t.Logf("injected into %d context(s)", n)
	if n < 1 {
		t.Fatalf("expected to inject into >=1 context, got %d", n)
	}
}

// loadSpikeCookies parses a Playwright storage_state JSON into chrome.Cookie
// rows. Only the fields livecdp needs are mapped.
func loadSpikeCookies(t *testing.T, path string) []chrome.Cookie {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
			SameSite string  `json:"sameSite"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	out := make([]chrome.Cookie, 0, len(state.Cookies))
	for _, c := range state.Cookies {
		ck := chrome.Cookie{
			HostKey: c.Domain,
			Name:    c.Name,
			Value:   c.Value,
			Path:    c.Path,
		}
		if c.Secure {
			ck.IsSecure = 1
		}
		if c.HTTPOnly {
			ck.IsHTTPOnly = 1
		}
		switch strings.ToLower(c.SameSite) {
		case "none":
			ck.SameSite = 0
		case "lax":
			ck.SameSite = 1
		case "strict":
			ck.SameSite = 2
		default:
			ck.SameSite = -1
		}
		out = append(out, ck)
	}
	return out
}
