package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

// normalizePullURL accepts a host, host:port, or full URL and returns a
// GET /pull URL. Bare hostnames default to port 9998 (the source pair/pull
// listener). No Muse-specific hosts are hardcoded.
func normalizePullURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("--pull-from is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse --pull-from: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("--pull-from scheme must be http or https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("--pull-from missing host")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPullListenPort)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/pull"
	}
	return u.String(), nil
}

func runSinkPullLoop(
	ctx context.Context,
	cfg *config.SinkConfig,
	transportSecret string,
	key []byte,
	seqTracker *protocol.SequenceTracker,
	stateWriter *state.Writer,
	sinkState *state.SinkState,
	stateMu *sync.Mutex,
	pullURL string,
	interval time.Duration,
) error {
	apply := func(sealed []byte) envelopeApplyResult {
		return applySealedEnvelope(ctx, cfg, transportSecret, key, seqTracker, stateWriter, sinkState, stateMu, sealed)
	}
	if err := pollSourceOnce(ctx, pullURL, transportSecret, apply); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie sink: pull: %v\n", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := pollSourceOnce(ctx, pullURL, transportSecret, apply); err != nil {
				fmt.Fprintf(os.Stderr, "agentcookie sink: pull: %v\n", err)
			}
		}
	}
}

func pollSourceOnce(ctx context.Context, pullURL, secret string, apply func([]byte) envelopeApplyResult) error {
	resolved := pullURL
	if r, err := resolveSinkURL(ctx, pullURL); err != nil {
		if errors.Is(err, tsclient.ErrAmbiguousPeer) {
			return fmt.Errorf("resolve pull URL: %w", err)
		}
	} else {
		resolved = r
	}

	reqCtx, cancel := context.WithTimeout(ctx, httpserver.Defaults(httpserver.PullClient).ClientTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, resolved, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if err := transport.SignRequest(req, secret, time.Now()); err != nil {
		return fmt.Errorf("sign pull request: %w", err)
	}
	resp, err := httpserver.Client(httpserver.PullClient).Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", resolved, err)
	}
	defer resp.Body.Close()

	maxBody := httpserver.Defaults(httpserver.SinkSync).MaxBodyBytes
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("read pull body: %w", err)
	}
	if int64(len(body)) > maxBody {
		return fmt.Errorf("pull body exceeds %d bytes", maxBody)
	}

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusOK:
		res := apply(body)
		if res.Status == http.StatusConflict {
			// Same sequence as last apply: skip, matching /sync replay defense.
			return nil
		}
		if res.Status != http.StatusOK {
			return fmt.Errorf("apply pulled envelope: %d: %s", res.Status, res.Reply)
		}
		return nil
	default:
		return fmt.Errorf("source returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}
