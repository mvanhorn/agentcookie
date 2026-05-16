package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/cdp"
)

var (
	verifySite      string
	verifyOutput    string
	verifyJSExpr    string
	verifyWaitSec   int
	verifyTabURL    string
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Drive the local managed Chrome to a URL and capture proof of authenticated state",
	Long: `verify connects to the sink's managed Chrome via DevTools Protocol,
opens a tab at --site, optionally evaluates a JS expression that should
return a truthy value when the user is logged in, and saves a screenshot
of the page.

Useful for demos and CI checks: "did the cookie sync actually leave us
authenticated on instacart.com after install?"

Typical agent flow:

  ssh <sink> 'agentcookie verify --site https://www.instacart.com/store \
    --js "!!document.querySelector('\''[aria-label*=\\\"account\\\"]'\'') " \
    --output /tmp/proof.png'

If --js is omitted, verify just navigates and screenshots; success means
the navigation completed without error and the screenshot exists.`,
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().StringVar(&verifySite, "site", "", "URL to navigate to (required)")
	verifyCmd.Flags().StringVar(&verifyOutput, "output", "", "path to write the screenshot PNG (default ~/.agentcookie/verify/<timestamp>.png)")
	verifyCmd.Flags().StringVar(&verifyJSExpr, "js", "", "optional JS expression to evaluate; must return truthy for logged-in check to pass")
	verifyCmd.Flags().IntVar(&verifyWaitSec, "wait", 5, "seconds to wait for page load before screenshot")
	verifyCmd.Flags().StringVar(&verifyTabURL, "managed-chrome-url", "", "override managed-Chrome WebSocket URL (default: read from ~/.agentcookie/chrome-profile/DevToolsActivePort)")
}

func runVerify(cmd *cobra.Command, args []string) error {
	if verifySite == "" {
		return fmt.Errorf("--site is required")
	}

	home, _ := os.UserHomeDir()
	if verifyOutput == "" {
		stamp := time.Now().UTC().Format("20060102-150405")
		verifyOutput = filepath.Join(home, ".agentcookie", "verify", "verify-"+stamp+".png")
	}
	if err := os.MkdirAll(filepath.Dir(verifyOutput), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	wsURL := verifyTabURL
	if wsURL == "" {
		port, wsPath, err := readDevToolsActivePort(filepath.Join(home, ".agentcookie", "chrome-profile", "DevToolsActivePort"))
		if err != nil {
			return fmt.Errorf("locate managed Chrome (is `agentcookie sink` running?): %w", err)
		}
		wsURL = fmt.Sprintf("ws://127.0.0.1:%d%s", port, wsPath)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	// Use the /json/new HTTP endpoint to create a new tab pointed at our URL,
	// then connect to that tab's WebSocket. This is the simplest path:
	// browser-level CDP cannot run Page.* commands without a target session,
	// and /json/new returns a ready-to-use tab webSocket URL.
	host, port := splitHostPortFromWSURL(wsURL)
	tabURL := fmt.Sprintf("http://%s:%d/json/new?%s", host, port, verifySite)
	tab, err := openNewTab(ctx, tabURL)
	if err != nil {
		return fmt.Errorf("open new tab via %s: %w", tabURL, err)
	}
	defer closeTab(host, port, tab.ID)

	conn, err := cdp.Dial(ctx, tab.WebSocketDebuggerURL)
	if err != nil {
		return fmt.Errorf("dial tab CDP %s: %w", tab.WebSocketDebuggerURL, err)
	}
	defer conn.Close()

	// Enable Page domain so we can use Page.captureScreenshot.
	if err := conn.Call(ctx, "Page.enable", map[string]any{}, nil); err != nil {
		return fmt.Errorf("Page.enable: %w", err)
	}

	// Wait for page load.
	time.Sleep(time.Duration(verifyWaitSec) * time.Second)

	if err := cdp.Screenshot(ctx, conn, verifyOutput, "png"); err != nil {
		return fmt.Errorf("capture screenshot: %w", err)
	}

	loggedIn := true
	var jsValue any
	if verifyJSExpr != "" {
		v, err := cdp.Evaluate(ctx, conn, verifyJSExpr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie verify: JS eval failed: %v\n", err)
			loggedIn = false
		} else {
			jsValue = v
			loggedIn = truthy(v)
		}
	}

	result := map[string]any{
		"site":          verifySite,
		"screenshot":    verifyOutput,
		"js_expr":       verifyJSExpr,
		"js_value":      jsValue,
		"logged_in":     loggedIn,
		"tab_target_id": tab.ID,
	}
	if common.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else {
		fmt.Fprintf(os.Stderr, "agentcookie verify: navigated to %s\n", verifySite)
		fmt.Fprintf(os.Stderr, "  screenshot: %s\n", verifyOutput)
		if verifyJSExpr != "" {
			fmt.Fprintf(os.Stderr, "  js result: %v (logged_in=%v)\n", jsValue, loggedIn)
		}
	}

	if verifyJSExpr != "" && !loggedIn {
		return fmt.Errorf("verify: JS predicate returned falsy; not logged in")
	}
	return nil
}

// tabInfo matches the JSON Chrome returns from /json/new and /json.
type tabInfo struct {
	ID                   string `json:"id"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	Type                 string `json:"type"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func openNewTab(ctx context.Context, url string) (*tabInfo, error) {
	// Chrome's /json/new endpoint requires PUT in newer versions.
	req, err := http.NewRequestWithContext(ctx, "PUT", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var t tabInfo
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode tab info: %w", err)
	}
	return &t, nil
}

func closeTab(host string, port int, id string) {
	url := fmt.Sprintf("http://%s:%d/json/close/%s", host, port, id)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func readDevToolsActivePort(path string) (int, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		return 0, "", fmt.Errorf("empty file")
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

func splitHostPortFromWSURL(wsURL string) (string, int) {
	// wsURL looks like ws://127.0.0.1:53310/devtools/browser/abc
	s := strings.TrimPrefix(wsURL, "ws://")
	s = strings.TrimPrefix(s, "wss://")
	hostPort := s
	if idx := strings.Index(s, "/"); idx >= 0 {
		hostPort = s[:idx]
	}
	host := hostPort
	port := 0
	if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
		host = hostPort[:idx]
		port, _ = strconv.Atoi(hostPort[idx+1:])
	}
	return host, port
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}
