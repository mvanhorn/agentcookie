package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/transport"
	"github.com/mvanhorn/agentcookie/internal/tsclient"
)

const defaultPullListenPort = "9998"

// pullCache holds the latest marshaled SyncEnvelope (plaintext JSON) so
// GET /pull can seal it per-peer. Source --watch keeps building envelopes
// exactly as before; this is the extra serving copy for client-only sinks.
//
// gen orders complete push cycles. Begin starts a cycle; StoreIfCurrent and
// ClearIfCurrent apply only if no newer cycle has begun, so a slower empty
// or fail-closed cycle cannot wipe a newer envelope.
type pullCache struct {
	mu      sync.RWMutex
	payload []byte
	gen     uint64
}

func newPullCache() *pullCache { return &pullCache{} }

func (c *pullCache) Begin() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	return c.gen
}

func (c *pullCache) Store(payload []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payload = append([]byte(nil), payload...)
}

func (c *pullCache) StoreIfCurrent(gen uint64, payload []byte) {
	if c == nil || gen == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.payload = append([]byte(nil), payload...)
}

func (c *pullCache) Load() []byte {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.payload) == 0 {
		return nil
	}
	return append([]byte(nil), c.payload...)
}

// Clear drops the cached envelope so GET /pull cannot serve cookies from a
// previous cycle after a fail-closed policy load or a cycle with nothing
// deliverable. 204 No Content is the empty/no-deliverable state.
func (c *pullCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.payload = nil
}

func (c *pullCache) ClearIfCurrent(gen uint64) {
	if c == nil || gen == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	c.payload = nil
}

// pullPayloadCache is the process-wide latest envelope for GET /pull.
// pushOnce publishes here after a successful marshal so watch and --once
// both leave a copy for inbound-blocked sinks.
var pullPayloadCache = newPullCache()

func pullAuthSecrets(configDir, legacy string) []string {
	var secrets []string
	peers, err := keystore.List(configDir)
	if err == nil {
		for _, peer := range peers {
			pk, loadErr := keystore.Load(configDir, peer)
			if loadErr != nil || pk == nil || len(pk.Key) == 0 {
				continue
			}
			secrets = append(secrets, string(pk.Key))
		}
	}
	if legacy != "" {
		secrets = append(secrets, legacy)
	}
	return secrets
}

func newPullHandler(cache *pullCache, secrets func() []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		httpserver.LimitedReader(r, httpserver.Defaults(httpserver.SourcePull).MaxBodyBytes)
		var secretList []string
		if secrets != nil {
			secretList = secrets()
		}
		matched, err := transport.VerifyRequestAny(r, secretList, time.Now(), 0)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		payload := cache.Load()
		if len(payload) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sealed, err := transport.SealWithSecret(payload, matched)
		if err != nil {
			http.Error(w, "seal payload: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sealed)
	})
}

func newPullServer(addr, configDir, legacy string, cache *pullCache) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/pull", newPullHandler(cache, func() []string {
		return pullAuthSecrets(configDir, legacy)
	}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	return httpserver.Configure(&http.Server{Addr: addr, Handler: mux}, httpserver.SourcePull)
}

func resolveSourcePullListen(ctx context.Context, explicit string) (string, error) {
	if explicit != "" {
		if err := validateListenAddr(explicit); err != nil {
			return "", fmt.Errorf("pull listen %q: %w", explicit, err)
		}
		return explicit, nil
	}
	ip, err := tsclient.RequireTailnetIP(ctx)
	if err != nil {
		return "", fmt.Errorf("detect Tailscale 100.x address for pull listener: %w", err)
	}
	return fmt.Sprintf("%s:%s", ip, defaultPullListenPort), nil
}

// startWatchPullListener serves GET /pull for the life of source --watch.
// Auto-detect failure (no Tailscale) logs a warning and leaves push intact;
// an explicit --pull-listen is required to bind and fails the command on error.
// The TCP bind happens synchronously so a colliding pairing port fails
// `source --watch` instead of looking successful while /pull is down.
func startWatchPullListener(ctx context.Context, cfg *config.SourceConfig) error {
	addr, err := resolveSourcePullListen(ctx, sourcePullListen)
	if err != nil {
		if sourcePullListen == "" {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: pull listener disabled (%v)\n", err)
			return nil
		}
		return err
	}
	legacy := ""
	if cfg != nil {
		legacy = cfg.Security.SharedSecret
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("pull listen %s: %w", addr, err)
	}
	srv := newPullServer(addr, common.ConfigDir, legacy, pullPayloadCache)
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	go func() {
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: serving GET /pull on http://%s/pull\n", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: pull listener: %v\n", err)
		}
	}()
	return nil
}
