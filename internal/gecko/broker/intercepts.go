package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/gecko/bidi"
	"github.com/mvanhorn/agentcookie/internal/gecko/socket"
	"net/url"
	"regexp"
	"sync"
	"time"
)

var noncePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)

type readIntercept struct {
	URL, Nonce, Context string
	IDs                 []string
}
type interceptTable struct {
	mu      sync.Mutex
	entries map[string]*readIntercept
	client  *bidi.Client
}

func newInterceptTable(b *bidi.Client) *interceptTable {
	return &interceptTable{entries: map[string]*readIntercept{}, client: b}
}
func (t *interceptTable) add(ctx context.Context, origin, nonce string) error {
	u, e := url.Parse(origin)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !noncePattern.MatchString(nonce) {
		return fmt.Errorf("invalid read origin or nonce")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) >= 64 {
		return fmt.Errorf("intercept limit reached")
	}
	if _, ok := t.entries[nonce]; ok {
		return fmt.Errorf("nonce already registered")
	}
	target := origin + "/.agentcookie-read/" + nonce
	var result struct {
		ID string `json:"intercept"`
	}
	if e = t.client.Call(ctx, "network.addIntercept", map[string]any{"phases": []string{"beforeRequestSent"}, "urlPatterns": []any{map[string]string{"type": "string", "pattern": target}}}, &result); e != nil {
		return e
	}
	t.entries[nonce] = &readIntercept{URL: target, Nonce: nonce, IDs: []string{result.ID}}
	return nil
}
func (t *interceptTable) remove(ctx context.Context, nonce string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[nonce]
	if entry == nil {
		return nil
	}
	for _, id := range entry.IDs {
		if e := t.client.Call(ctx, "network.removeIntercept", map[string]string{"intercept": id}, nil); e != nil {
			return e
		}
	}
	delete(t.entries, nonce)
	return nil
}
func (t *interceptTable) handle(m socket.Message) {
	if m.Method != "network.beforeRequestSent" {
		return
	}
	var p struct {
		Context string `json:"context"`
		Blocked bool   `json:"isBlocked"`
		Request struct {
			ID  string `json:"request"`
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(m.Params, &p) != nil || !p.Blocked {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, entry := range t.entries {
		if p.Request.URL == entry.URL && entry.Context == "" {
			// Install context-wide interception BEFORE releasing the document. The helper
			// gets a restrictive CSP, and every subsequent request in its context fails.
			var result struct {
				ID string `json:"intercept"`
			}
			if p.Context == "" || t.client.Call(ctx, "network.addIntercept", map[string]any{"phases": []string{"beforeRequestSent"}, "contexts": []string{p.Context}}, &result) != nil {
				_ = t.client.Call(ctx, "network.failRequest", map[string]string{"request": p.Request.ID}, nil)
				return
			}
			entry.Context = p.Context
			entry.IDs = append(entry.IDs, result.ID)
			html := "<!doctype html><title>agentcookie " + entry.Nonce + "</title>"
			headers := []any{map[string]any{"name": "Content-Type", "value": map[string]string{"type": "string", "value": "text/html; charset=utf-8"}}, map[string]any{"name": "Content-Security-Policy", "value": map[string]string{"type": "string", "value": "default-src 'none'"}}, map[string]any{"name": "Cache-Control", "value": map[string]string{"type": "string", "value": "no-store"}}}
			_ = t.client.Call(ctx, "network.provideResponse", map[string]any{"request": p.Request.ID, "statusCode": 200, "headers": headers, "body": map[string]string{"type": "base64", "value": base64.StdEncoding.EncodeToString([]byte(html))}}, nil)
			return
		}
		if entry.Context == p.Context && p.Context != "" {
			_ = t.client.Call(ctx, "network.failRequest", map[string]string{"request": p.Request.ID}, nil)
			return
		}
	}
}
func (c *Client) AddReadIntercept(ctx context.Context, origin, nonce string) error {
	return c.request(ctx, "read-intercept", map[string]string{"origin": origin, "nonce": nonce}, nil)
}
func (c *Client) RemoveReadIntercept(ctx context.Context, nonce string) error {
	return c.request(ctx, "remove-read-intercept", map[string]string{"nonce": nonce}, nil)
}
