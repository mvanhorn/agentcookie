// Package extbridge is the localhost HTTP queue between agentcookie-sink and
// the agentcookie Chrome extension. Sink enqueues cookie batches; the
// extension long-polls /extension/cookies/pending and posts per-cookie
// results to /extension/cookies/result. Sink waits up to 30s for results,
// then surfaces per-cookie success counts back to the source.
//
// The channel is loopback-only and auth-gated by a per-install token shared
// between the sink and the extension's chrome.storage.local entry.
package extbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// Bridge is the in-process queue. Construct via New, mount handlers on a mux
// via Mount, push cookies via SetCookies.
type Bridge struct {
	token        string
	pendingCh    chan *batch
	mu           sync.Mutex
	awaitingResp map[string]*batchPromise
	pollTimeout  time.Duration
	resultWait   time.Duration
}

type batch struct {
	BatchID string    `json:"batch_id"`
	Cookies []wireCookie `json:"cookies"`
}

// wireCookie is the JSON shape the extension receives. Field names mirror
// chrome.Cookie's json tags so the extension's JS reads natural keys.
type wireCookie struct {
	HostKey       string `json:"host_key"`
	Name          string `json:"name"`
	Value         string `json:"value"`
	Path          string `json:"path"`
	ExpiresUTC    int64  `json:"expires_utc"`
	IsSecure      int    `json:"is_secure"`
	IsHTTPOnly    int    `json:"is_httponly"`
	LastAccessUTC int64  `json:"last_access_utc"`
	HasExpires    int    `json:"has_expires"`
	IsPersistent  int    `json:"is_persistent"`
	Priority      int    `json:"priority"`
	SameSite      int    `json:"samesite"`
	SourceScheme  int    `json:"source_scheme"`
	SourcePort    int    `json:"source_port"`
}

type batchPromise struct {
	done    chan struct{}
	results []ExtensionResult
}

// ExtensionResult is one cookie's outcome reported by the extension.
type ExtensionResult struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Config knobs.
type Config struct {
	// Token gates the HTTP endpoints; must match the extension's storage.
	Token string
	// PollTimeout caps how long the extension's GET /pending blocks for.
	PollTimeout time.Duration
	// ResultWait caps how long the sink waits for the extension to finish
	// processing a batch before returning a partial result.
	ResultWait time.Duration
}

// New returns a Bridge ready to attach handlers and accept batches.
func New(cfg Config) *Bridge {
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = 25 * time.Second
	}
	if cfg.ResultWait == 0 {
		cfg.ResultWait = 30 * time.Second
	}
	return &Bridge{
		token:        cfg.Token,
		pendingCh:    make(chan *batch, 16),
		awaitingResp: map[string]*batchPromise{},
		pollTimeout:  cfg.PollTimeout,
		resultWait:   cfg.ResultWait,
	}
}

// Mount registers the /extension/cookies/* HTTP endpoints onto mux.
func (b *Bridge) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/extension/cookies/pending", b.handlePending)
	mux.HandleFunc("/extension/cookies/result", b.handleResult)
}

// SetCookies enqueues cookies for the extension to write and waits up to
// ResultWait for the per-cookie results. Returns the count of successes plus
// the per-host failure breakdown. If the extension never picks up the batch
// (e.g., Chrome is crashed), returns context.DeadlineExceeded as the error.
func (b *Bridge) SetCookies(ctx context.Context, cookies []chrome.Cookie) (int, map[string]int, error) {
	if len(cookies) == 0 {
		return 0, nil, nil
	}

	wire := make([]wireCookie, 0, len(cookies))
	for _, c := range cookies {
		wire = append(wire, wireCookie{
			HostKey: c.HostKey, Name: c.Name, Value: c.Value, Path: c.Path,
			ExpiresUTC: c.ExpiresUTC, IsSecure: c.IsSecure, IsHTTPOnly: c.IsHTTPOnly,
			LastAccessUTC: c.LastAccessUTC, HasExpires: c.HasExpires, IsPersistent: c.IsPersistent,
			Priority: c.Priority, SameSite: c.SameSite, SourceScheme: c.SourceScheme,
			SourcePort: c.SourcePort,
		})
	}

	id := uuid.New().String()
	bt := &batch{BatchID: id, Cookies: wire}
	promise := &batchPromise{done: make(chan struct{})}

	b.mu.Lock()
	b.awaitingResp[id] = promise
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.awaitingResp, id)
		b.mu.Unlock()
	}()

	// Send the batch into the queue. If the extension isn't polling, this
	// blocks up to ResultWait.
	select {
	case b.pendingCh <- bt:
	case <-time.After(b.resultWait):
		return 0, nil, fmt.Errorf("extension never picked up batch (sink queue full or extension not running)")
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}

	// Wait for extension to post results.
	select {
	case <-promise.done:
	case <-time.After(b.resultWait):
		return 0, nil, fmt.Errorf("extension did not report results within %s", b.resultWait)
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}

	accepted := 0
	failures := map[string]int{}
	for _, r := range promise.results {
		if r.Success {
			accepted++
			continue
		}
		failures[r.Host]++
	}
	return accepted, failures, nil
}

func (b *Bridge) handlePending(w http.ResponseWriter, r *http.Request) {
	if !b.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), b.pollTimeout)
	defer cancel()
	select {
	case bt := <-b.pendingCh:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bt)
	case <-ctx.Done():
		// No work within the long-poll window. The extension reconnects.
		w.WriteHeader(http.StatusNoContent)
	}
}

func (b *Bridge) handleResult(w http.ResponseWriter, r *http.Request) {
	if !b.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		BatchID string            `json:"batch_id"`
		Results []ExtensionResult `json:"results"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	promise, ok := b.awaitingResp[payload.BatchID]
	b.mu.Unlock()
	if !ok {
		// Late or duplicate result; benign.
		w.WriteHeader(http.StatusOK)
		return
	}
	promise.results = payload.Results
	close(promise.done)
	w.WriteHeader(http.StatusOK)
}

func (b *Bridge) authed(r *http.Request) bool {
	return r.Header.Get("X-AgentCookie-Token") == b.token
}
