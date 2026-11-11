// Package bidi owns only sessions it creates. A lost direct BiDi connection
// cannot be recovered by replaying a saved session ID.
package bidi

import (
	"context"
	"fmt"
	"github.com/gobwas/ws"
	"github.com/mvanhorn/agentcookie/internal/gecko/socket"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
)

type Client struct {
	peer       *socket.Peer
	mu         sync.Mutex
	owned      bool
	Profile    string
	SessionID  string
	BrowserPID int
}

func Dial(ctx context.Context, endpoint string) (*Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "ws" || u.User != nil || u.RawQuery != "" || !loopback(u.Hostname()) {
		return nil, fmt.Errorf("BiDi endpoint must be loopback ws URL")
	}
	if _, e = strconv.ParseUint(u.Port(), 10, 16); e != nil {
		return nil, fmt.Errorf("BiDi endpoint requires port")
	}
	conn, br, _, e := ws.Dial(ctx, endpoint)
	if e != nil {
		return nil, e
	}
	if br != nil && br.Buffered() > 0 {
		conn.Close()
		return nil, fmt.Errorf("unexpected data before BiDi handshake")
	}
	return &Client{peer: socket.New(conn, ws.StateClientSide)}, nil
}
func loopback(h string) bool { ip := net.ParseIP(h); return ip != nil && ip.IsLoopback() }
func (c *Client) NewSession(ctx context.Context, profile string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned {
		return fmt.Errorf("session already owned")
	}
	var result struct {
		SessionID    string         `json:"sessionId"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if e := c.peer.Call(ctx, "session.new", map[string]any{"capabilities": map[string]any{}}, &result); e != nil {
		return e
	}
	c.owned = true
	c.SessionID = result.SessionID
	if pid, ok := result.Capabilities["moz:processID"].(float64); ok {
		c.BrowserPID = int(pid)
	}
	reported, _ := result.Capabilities["moz:profile"].(string)
	actual, e := filepath.EvalSymlinks(reported)
	expected, ee := filepath.EvalSymlinks(profile)
	if e != nil || ee != nil || actual != expected {
		_ = c.peer.Call(ctx, "session.end", map[string]any{}, nil)
		c.owned = false
		return fmt.Errorf("BiDi profile mismatch: refusing companion installation")
	}
	c.Profile = actual
	return nil
}
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	return c.peer.Call(ctx, method, params, out)
}
func (c *Client) Install(ctx context.Context, path string) error {
	return c.Call(ctx, "webExtension.install", map[string]any{"extensionData": map[string]any{"type": "path", "path": path}}, nil)
}
func (c *Client) Events() <-chan socket.Message { return c.peer.Events() }
func (c *Client) Done() <-chan struct{}         { return c.peer.Done() }
func (c *Client) End(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.owned {
		return nil
	}
	e := c.Call(ctx, "session.end", map[string]any{}, nil)
	if e == nil {
		c.owned = false
	}
	return e
}

// Close disconnects without ending the session. Only the broker should call End.
func (c *Client) Close() error { return c.peer.Close() }
