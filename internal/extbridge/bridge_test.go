package extbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestBridgeRequiresAuthToken(t *testing.T) {
	b := New(Config{Token: "secret"})
	mux := http.NewServeMux()
	b.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// No auth header.
	resp, err := http.Get(srv.URL + "/extension/cookies/pending")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Wrong token.
	req, _ := http.NewRequest("GET", srv.URL+"/extension/cookies/pending", nil)
	req.Header.Set("X-AgentCookie-Token", "wrong")
	resp2, _ := http.DefaultClient.Do(req)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong token, got %d", resp2.StatusCode)
	}
}

func TestBridgeRoundTrip(t *testing.T) {
	b := New(Config{Token: "secret", PollTimeout: 2 * time.Second, ResultWait: 3 * time.Second})
	mux := http.NewServeMux()
	b.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Simulate the extension: poll for a batch, respond with results.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("GET", srv.URL+"/extension/cookies/pending", nil)
		req.Header.Set("X-AgentCookie-Token", "secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("poll: %v", err)
			return
		}
		defer resp.Body.Close()
		var bt batch
		if err := json.NewDecoder(resp.Body).Decode(&bt); err != nil {
			t.Errorf("decode batch: %v", err)
			return
		}
		// Reply with success for every cookie.
		results := make([]ExtensionResult, len(bt.Cookies))
		for i, c := range bt.Cookies {
			results[i] = ExtensionResult{Name: c.Name, Host: c.HostKey, Success: true}
		}
		payload, _ := json.Marshal(map[string]any{"batch_id": bt.BatchID, "results": results})
		postReq, _ := http.NewRequest("POST", srv.URL+"/extension/cookies/result", bytes.NewReader(payload))
		postReq.Header.Set("Content-Type", "application/json")
		postReq.Header.Set("X-AgentCookie-Token", "secret")
		postResp, err := http.DefaultClient.Do(postReq)
		if err != nil {
			t.Errorf("post results: %v", err)
			return
		}
		_, _ = io.Copy(io.Discard, postResp.Body)
		postResp.Body.Close()
	}()

	cookies := []chrome.Cookie{
		{HostKey: ".github.com", Name: "a", Value: "1"},
		{HostKey: "instacart.com", Name: "b", Value: "2"},
	}
	accepted, failures, err := b.SetCookies(context.Background(), cookies)
	if err != nil {
		t.Fatalf("SetCookies: %v", err)
	}
	if accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", accepted)
	}
	if len(failures) != 0 {
		t.Errorf("expected no failures, got %v", failures)
	}
	<-done
}

func TestBridgeTimesOutWithoutExtension(t *testing.T) {
	b := New(Config{Token: "secret", PollTimeout: 200 * time.Millisecond, ResultWait: 500 * time.Millisecond})
	// No HTTP server; just call SetCookies. The pending channel is full at
	// capacity 16; we push N+1 and the 17th must time out.
	// Actually easier: ResultWait fires waiting for a result that never comes.
	cookies := []chrome.Cookie{{HostKey: "a.com", Name: "x", Value: "1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := b.SetCookies(ctx, cookies)
	if err == nil {
		t.Fatal("expected timeout when no extension is running")
	}
}

func TestBridgeReportsFailures(t *testing.T) {
	b := New(Config{Token: "secret", PollTimeout: 2 * time.Second, ResultWait: 3 * time.Second})
	mux := http.NewServeMux()
	b.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	go func() {
		req, _ := http.NewRequest("GET", srv.URL+"/extension/cookies/pending", nil)
		req.Header.Set("X-AgentCookie-Token", "secret")
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		var bt batch
		_ = json.NewDecoder(resp.Body).Decode(&bt)
		// Half succeed, half fail.
		results := make([]ExtensionResult, len(bt.Cookies))
		for i, c := range bt.Cookies {
			r := ExtensionResult{Name: c.Name, Host: c.HostKey}
			if i%2 == 0 {
				r.Success = true
			} else {
				r.Error = "synthetic failure"
			}
			results[i] = r
		}
		payload, _ := json.Marshal(map[string]any{"batch_id": bt.BatchID, "results": results})
		postReq, _ := http.NewRequest("POST", srv.URL+"/extension/cookies/result", bytes.NewReader(payload))
		postReq.Header.Set("X-AgentCookie-Token", "secret")
		http.DefaultClient.Do(postReq)
	}()

	cookies := []chrome.Cookie{
		{HostKey: "a.com", Name: "ok1", Value: "1"},
		{HostKey: "b.com", Name: "fail1", Value: "2"},
		{HostKey: "a.com", Name: "ok2", Value: "3"},
		{HostKey: "b.com", Name: "fail2", Value: "4"},
	}
	accepted, failures, err := b.SetCookies(context.Background(), cookies)
	if err != nil {
		t.Fatalf("SetCookies: %v", err)
	}
	if accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", accepted)
	}
	if failures["b.com"] != 2 {
		t.Errorf("expected b.com to have 2 failures, got %v", failures)
	}
}
