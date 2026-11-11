// Package e2e provides hermetic browser fixtures. It never discovers profiles,
// reads credentials, or starts a browser with its default user-data-dir.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/credentialguard"
	"github.com/mvanhorn/agentcookie/internal/source"
)

const FixturePassword = "agentcookie-synthetic-cookie-key"

type Harness struct {
	Home       string
	Endpoint   string
	ProfileDir string
	t          *testing.T
}

func New(t *testing.T) *Harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("TMPDIR", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "blocklist.yaml"), []byte("version: 1\npolicy: allowlist\ndomains:\n  - pattern: '%localhost'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentialguard.ForbidRealProviders())
	return &Harness{Home: home, t: t}
}

// ChromiumFixture writes a closed, synthetic Chromium Cookies database. It
// contains only caller-provided records encrypted with the fixture password.
func (h *Harness) ChromiumFixture(cookies []chrome.Cookie) *config.SourceConfig {
	h.t.Helper()
	path := filepath.Join(h.Home, "source-profile", "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		h.t.Fatal(err)
	}
	if _, err := chrome.WriteCookiesSidecar(path, nil, nil); err != nil {
		h.t.Fatal(err)
	}
	key, err := chrome.DeriveAESKey(FixturePassword)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err = chrome.WriteCookies(path, cookies, key); err != nil {
		h.t.Fatal(err)
	}
	return &config.SourceConfig{Chrome: config.ChromeRef{DBPath: path}, Browser: config.BrowserRef{Name: "chrome"}}
}
func (h *Harness) Reader(cfg *config.SourceConfig) source.Reader {
	h.t.Helper()
	r, err := source.New(cfg, source.Options{}, source.Dependencies{Password: func(chrome.Browser) (string, error) { return FixturePassword, nil }})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = r.Close() })
	return r
}

// Browser starts and owns a throwaway Chromium; cleanup gracefully exits and
// then cancels the allocator even after test failures. No real profile is read.
func (h *Harness) Browser() context.Context {
	h.t.Helper()
	binary := os.Getenv("AGENTCOOKIE_CHROME_BINARY")
	if binary == "" {
		for _, name := range []string{"chromium", "google-chrome", "chromium-browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"} {
			if p, err := exec.LookPath(name); err == nil {
				binary = p
				break
			}
		}
	}
	if binary == "" {
		if os.Getenv("AGENTCOOKIE_E2E_REQUIRED") == "1" {
			h.t.Fatal("fixture Chromium is required")
		}
		h.t.Skip("fixture Chromium unavailable; set AGENTCOOKIE_CHROME_BINARY or AGENTCOOKIE_E2E_REQUIRED=1")
	}
	profile, err := os.MkdirTemp(h.Home, "destination-chromium-")
	if err != nil {
		h.t.Fatal(err)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(binary), chromedp.UserDataDir(profile), chromedp.Flag("use-mock-keychain", true), chromedp.Flag("password-store", "basic"), chromedp.Flag("remote-debugging-address", "127.0.0.1"), chromedp.Flag("remote-debugging-port", "0"), chromedp.Flag("no-first-run", true), chromedp.Flag("no-default-browser-check", true), chromedp.Flag("disable-dev-shm-usage", true))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelBrowser := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 5*time.Minute)
	h.t.Cleanup(func() { _ = chromedp.Cancel(ctx); cancelTimeout(); cancelBrowser(); cancelAlloc() })
	if err := chromedp.Run(ctx); err != nil {
		h.t.Fatal(fmt.Errorf("start fixture Chromium: %w", err))
	}
	data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		h.t.Fatal(err)
	}
	port, _, _ := strings.Cut(string(data), "\n")
	h.Endpoint = "http://127.0.0.1:" + port
	h.ProfileDir = profile
	return ctx
}
