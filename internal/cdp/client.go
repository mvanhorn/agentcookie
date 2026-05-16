// Package cdp is a minimal Chrome DevTools Protocol client tailored to the
// cookie-injection use case. It can probe whether Chrome is running with
// remote debugging enabled and call Storage.setCookies over a single
// browser-level WebSocket connection. It does not aim to be a general CDP
// client; chromedp covers that ground with a much larger dependency footprint.
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// VersionInfo is the shape returned by Chrome's /json/version endpoint.
type VersionInfo struct {
	Browser              string `json:"Browser"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Probe checks whether Chrome is running with --remote-debugging-port on the
// given host:port. Returns the browser-level webSocketDebuggerUrl, or an
// error if no debuggable Chrome is reachable.
func Probe(ctx context.Context, host string, port int) (*VersionInfo, error) {
	url := fmt.Sprintf("http://%s:%d/json/version", host, port)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("probe %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	var v VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode /json/version: %w", err)
	}
	if v.WebSocketDebuggerURL == "" {
		return nil, fmt.Errorf("Chrome at %s:%d returned no webSocketDebuggerUrl", host, port)
	}
	return &v, nil
}

// Conn is a thin CDP WebSocket client. It serializes one method call at a time
// (write-side mutex) and demultiplexes responses by id from a single reader
// goroutine.
type Conn struct {
	ws *websocket.Conn

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan rawResponse
	closed  bool
}

type request struct {
	ID     int64       `json:"id"`
	Method string      `json:"method"`
	Params interface{} `json:"params,omitempty"`
}

type rawResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *errorObj       `json:"error,omitempty"`
}

type errorObj struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// Dial opens a WebSocket to the given browser-level debugger URL.
func Dial(ctx context.Context, wsURL string) (*Conn, error) {
	dialer := *websocket.DefaultDialer
	ws, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial cdp %s: %w", wsURL, err)
	}
	c := &Conn{
		ws:      ws,
		pending: make(map[int64]chan rawResponse),
	}
	go c.readLoop()
	return c, nil
}

// readLoop pumps inbound messages and routes responses to their waiting
// goroutines. CDP events (no id) are dropped; this client doesn't subscribe.
func (c *Conn) readLoop() {
	defer c.cleanup()
	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var r rawResponse
		if err := json.Unmarshal(msg, &r); err != nil {
			continue
		}
		if r.ID == 0 {
			continue
		}
		c.pendMu.Lock()
		ch, ok := c.pending[r.ID]
		if ok {
			delete(c.pending, r.ID)
		}
		c.pendMu.Unlock()
		if ok {
			ch <- r
			close(ch)
		}
	}
}

func (c *Conn) cleanup() {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// Call sends a method and waits for the response. If result is non-nil, the
// response's "result" field is decoded into it.
func (c *Conn) Call(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := c.nextID.Add(1)
	ch := make(chan rawResponse, 1)

	c.pendMu.Lock()
	if c.closed {
		c.pendMu.Unlock()
		return fmt.Errorf("cdp connection closed")
	}
	c.pending[id] = ch
	c.pendMu.Unlock()

	req := request{ID: id, Method: method, Params: params}
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}

	c.writeMu.Lock()
	err = c.ws.WriteMessage(websocket.TextMessage, data)
	c.writeMu.Unlock()
	if err != nil {
		// Drain the pending entry on write failure so it doesn't leak.
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.pendMu.Lock()
		delete(c.pending, id)
		c.pendMu.Unlock()
		return ctx.Err()
	case r, ok := <-ch:
		if !ok {
			return fmt.Errorf("cdp connection closed waiting for %s response", method)
		}
		if r.Error != nil {
			return fmt.Errorf("cdp %s: %s (code %d)", method, r.Error.Message, r.Error.Code)
		}
		if result != nil && len(r.Result) > 0 {
			if err := json.Unmarshal(r.Result, result); err != nil {
				return fmt.Errorf("decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Close shuts down the WebSocket. Safe to call multiple times.
func (c *Conn) Close() error {
	return c.ws.Close()
}
