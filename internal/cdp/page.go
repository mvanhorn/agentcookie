package cdp

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
)

// Navigate calls Page.navigate on the given target.
func Navigate(ctx context.Context, conn *Conn, url string) error {
	var result struct {
		FrameID  string `json:"frameId"`
		ErrorTxt string `json:"errorText"`
	}
	if err := conn.Call(ctx, "Page.navigate", map[string]any{"url": url}, &result); err != nil {
		return fmt.Errorf("Page.navigate %s: %w", url, err)
	}
	if result.ErrorTxt != "" {
		return fmt.Errorf("Page.navigate %s: %s", url, result.ErrorTxt)
	}
	return nil
}

// Screenshot captures the current viewport and writes a PNG to path. format
// must be "png" or "jpeg".
func Screenshot(ctx context.Context, conn *Conn, path string, format string) error {
	if format == "" {
		format = "png"
	}
	var result struct {
		Data string `json:"data"`
	}
	if err := conn.Call(ctx, "Page.captureScreenshot", map[string]any{"format": format}, &result); err != nil {
		return fmt.Errorf("Page.captureScreenshot: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return fmt.Errorf("decode screenshot: %w", err)
	}
	return os.WriteFile(path, raw, 0o644)
}

// Evaluate runs a JS expression in the page's default execution context and
// returns the result as a generic any. Use this for "is the user logged in"
// queries against the DOM.
func Evaluate(ctx context.Context, conn *Conn, expression string) (any, error) {
	var result struct {
		Result struct {
			Type        string `json:"type"`
			Value       any    `json:"value"`
			Description string `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text       string `json:"text"`
			Exception  any    `json:"exception"`
		} `json:"exceptionDetails,omitempty"`
	}
	args := map[string]any{
		"expression":            expression,
		"returnByValue":         true,
		"awaitPromise":          true,
		"timeout":               10000,
	}
	if err := conn.Call(ctx, "Runtime.evaluate", args, &result); err != nil {
		return nil, fmt.Errorf("Runtime.evaluate: %w", err)
	}
	if result.ExceptionDetails != nil {
		return nil, fmt.Errorf("JS exception: %s", result.ExceptionDetails.Text)
	}
	return result.Result.Value, nil
}

// AttachToPage creates a new tab and returns a session-attached Conn that can
// run Page-level commands. For v0.3 demo purposes we use the existing browser
// connection directly with a Target.createTarget call followed by attaching.
// The simpler path: open a new tab via Target.createTarget and connect to
// THAT tab's webSocketDebuggerUrl. This function returns the new tab's
// session id so subsequent Page.* calls can be scoped.
type Target struct {
	TargetID             string `json:"targetId"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// CreateTarget opens a new tab with the given URL and returns its target id.
func CreateTarget(ctx context.Context, conn *Conn, url string) (string, error) {
	var result struct {
		TargetID string `json:"targetId"`
	}
	if err := conn.Call(ctx, "Target.createTarget", map[string]any{"url": url}, &result); err != nil {
		return "", fmt.Errorf("Target.createTarget: %w", err)
	}
	return result.TargetID, nil
}

// CloseTarget closes the given tab.
func CloseTarget(ctx context.Context, conn *Conn, targetID string) error {
	return conn.Call(ctx, "Target.closeTarget", map[string]any{"targetId": targetID}, nil)
}

// AttachToTarget creates a session attached to the given target. Returns the
// session id; subsequent Page.* / Runtime.* calls must include sessionId.
func AttachToTarget(ctx context.Context, conn *Conn, targetID string) (string, error) {
	var result struct {
		SessionID string `json:"sessionId"`
	}
	args := map[string]any{"targetId": targetID, "flatten": true}
	if err := conn.Call(ctx, "Target.attachToTarget", args, &result); err != nil {
		return "", fmt.Errorf("Target.attachToTarget: %w", err)
	}
	return result.SessionID, nil
}
