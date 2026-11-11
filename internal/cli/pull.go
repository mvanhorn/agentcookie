package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/cli/httpserver"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/pairing"
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
	// changedAt is when a stored envelope last differed from the one
	// before it; the teammate staleness notice compares pulls against it.
	changedAt time.Time
}

// ChangedAt returns when the served envelope last changed (zero before
// the first one).
func (c *pullCache) ChangedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.changedAt
}

// setLocked stores payload and notes a change. c.mu must be held.
func (c *pullCache) setLocked(payload []byte) {
	if !bytes.Equal(c.payload, payload) {
		c.changedAt = time.Now()
	}
	c.payload = append([]byte(nil), payload...)
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
	c.setLocked(payload)
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
	c.setLocked(payload)
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

// peerSecret is one credential /pull accepts: a paired peer's key (name
// is its keys/ filename) or the legacy shared secret.
type peerSecret struct {
	name   string
	secret string
	legacy bool
}

// pullAuthPeers lists every paired key plus the legacy shared secret.
func pullAuthPeers(configDir, legacy string) []peerSecret {
	var out []peerSecret
	peers, err := keystore.List(configDir)
	if err == nil {
		for _, peer := range peers {
			pk, loadErr := keystore.Load(configDir, peer)
			if loadErr != nil || pk == nil || len(pk.Key) == 0 {
				continue
			}
			out = append(out, peerSecret{name: peer, secret: string(pk.Key)})
		}
	}
	if legacy != "" {
		out = append(out, peerSecret{secret: legacy, legacy: true})
	}
	return out
}

func pullAuthSecrets(configDir, legacy string) []string {
	var secrets []string
	for _, p := range pullAuthPeers(configDir, legacy) {
		secrets = append(secrets, p.secret)
	}
	return secrets
}

// newPullHandler serves /pull to any holder of one of secrets (the
// pre-teammate rule).
func newPullHandler(cache *pullCache, secrets func() []string) http.Handler {
	return newPullHandlerWithAuth(cache, legacySecretsAuth(secrets))
}

func newPullHandlerWithAuth(cache *pullCache, auth pullAuth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		httpserver.LimitedReader(r, httpserver.Defaults(httpserver.SourcePull).MaxBodyBytes)
		d := auth.authorizePull(r)
		if d.status != 0 {
			http.Error(w, d.msg, d.status)
			return
		}
		payload := cache.Load()
		if len(payload) == 0 {
			w.WriteHeader(http.StatusNoContent)
			if d.served != nil {
				d.served()
			}
			return
		}
		var envelope protocol.SyncEnvelope
		if json.Unmarshal(payload, &envelope) == nil && envelope.Snapshot != nil {
			if r.Header.Get("X-Agentcookie-Protocol-Version") == "3" {
				envelope.ProtocolVersion = 3
				envelope.LocalStorageTarball = nil
				envelope.IndexedDBTarball = nil
				envelope.IndexedDBSkipped = nil
			} else {
				envelope.ProtocolVersion = 2
				envelope.Snapshot = nil
				envelope.Storage = nil
				envelope.Destination = ""
				envelope.SourceBrowser = ""
				envelope.ReaderMode = ""
				if len(envelope.Cookies) == 0 && len(envelope.LocalStorageTarball) == 0 && len(envelope.IndexedDBTarball) == 0 && len(envelope.Secrets) == 0 {
					w.WriteHeader(http.StatusNoContent)
					if d.served != nil {
						d.served()
					}
					return
				}
			}
			payload, _ = json.Marshal(envelope)
		}
		sealed, err := transport.SealWithSecret(payload, d.secret)
		if err != nil {
			http.Error(w, "seal payload: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(sealed)
		if d.served != nil {
			d.served()
		}
	})
}

// newPullMux builds the 9998 handler once, so a rebind on a tailnet IP
// change keeps serving the same state.
func newPullMux(configDir, legacy string, cache *pullCache, hub *teammateHub) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/pull", newPullHandlerWithAuth(cache, newPullAuthorizer(configDir, legacy, hub)))
	if hub != nil {
		// Teammate pairing (KTD7) shares this server so it never fights
		// `source --watch` for port 9998. It is a 404 unless a teammate
		// pairing is pending, as the route was before it existed.
		mux.Handle("/pair", hub.pairHandler(pairing.LocalHostname()))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	return mux
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

// Pull listener seams. Production binds this machine's Tailscale 100.x
// address on port 9998; tests swap these with
// setPullListenerSeamsForTesting.
var (
	pullListenIPFunc        = tsclient.RequireTailnetIP
	pullListenPort          = defaultPullListenPort
	pullListenRetryMin      = 2 * time.Second
	pullListenRetryMax      = time.Minute
	pullListenCheckInterval = 30 * time.Second
)

func setPullListenerSeamsForTesting(ipFn func(context.Context) (string, error), port string, retryMin, check time.Duration) func() {
	prevIP, prevPort, prevMin, prevMax, prevCheck := pullListenIPFunc, pullListenPort, pullListenRetryMin, pullListenRetryMax, pullListenCheckInterval
	pullListenIPFunc, pullListenPort, pullListenRetryMin, pullListenRetryMax, pullListenCheckInterval = ipFn, port, retryMin, 4*retryMin, check
	return func() {
		pullListenIPFunc, pullListenPort, pullListenRetryMin, pullListenRetryMax, pullListenCheckInterval = prevIP, prevPort, prevMin, prevMax, prevCheck
	}
}

// startWatchPullListener serves GET /pull for the life of source --watch.
// An explicit --pull-listen binds once and fails the command on error. The
// default tailnet address is supervised (KTD9): when Tailscale is not up
// yet the listener retries with backoff instead of staying off for the
// life of the daemon, and it rebinds when the tailnet IP changes. A
// colliding port at startup still fails `source --watch` so a held pairing
// port is not mistaken for a working /pull. onStatus (may be nil) reports
// every up/down change.
func startWatchPullListener(ctx context.Context, cfg *config.SourceConfig, onStatus func(up bool, addr string)) error {
	if onStatus == nil {
		onStatus = func(bool, string) {}
	}
	legacy := ""
	if cfg != nil {
		legacy = cfg.Security.SharedSecret
	}
	handler := newPullMux(common.ConfigDir, legacy, pullPayloadCache, activeTeammates)
	if sourcePullListen != "" {
		addr, err := resolveSourcePullListen(ctx, sourcePullListen)
		if err != nil {
			return err
		}
		if _, err := servePull(ctx, addr, handler); err != nil {
			return err
		}
		onStatus(true, addr)
		return nil
	}
	sup := &pullListenerSupervisor{
		handler:  handler,
		onStatus: onStatus,
		ipFn:     pullListenIPFunc,
		port:     pullListenPort,
		retryMin: pullListenRetryMin,
		retryMax: pullListenRetryMax,
		check:    pullListenCheckInterval,
	}
	ip, err := sup.ipFn(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: pull listener waiting for Tailscale (%v); retrying\n", err)
		onStatus(false, "")
	} else if err := sup.bind(ctx, ip); err != nil {
		return err
	}
	go sup.run(ctx)
	return nil
}

// servePull binds addr and serves handler until ctx ends or the returned
// server is shut down.
func servePull(ctx context.Context, addr string, handler http.Handler) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pull listen %s: %w", addr, err)
	}
	srv := httpserver.Configure(&http.Server{Addr: ln.Addr().String(), Handler: handler}, httpserver.SourcePull)
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
	return srv, nil
}

// pullListenerSupervisor keeps the default /pull listener bound to the
// current tailnet IP.
type pullListenerSupervisor struct {
	handler  http.Handler
	onStatus func(up bool, addr string)
	ipFn     func(context.Context) (string, error)
	port     string
	retryMin time.Duration
	retryMax time.Duration
	check    time.Duration

	ip     string
	srv    *http.Server
	cancel context.CancelFunc
}

func (s *pullListenerSupervisor) bind(ctx context.Context, ip string) error {
	srvCtx, cancel := context.WithCancel(ctx)
	srv, err := servePull(srvCtx, net.JoinHostPort(ip, s.port), s.handler)
	if err != nil {
		cancel()
		return err
	}
	s.ip, s.srv, s.cancel = ip, srv, cancel
	s.onStatus(true, srv.Addr)
	return nil
}

func (s *pullListenerSupervisor) down() {
	if s.srv == nil {
		return
	}
	s.cancel()
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(shutCtx)
	s.ip, s.srv, s.cancel = "", nil, nil
	s.onStatus(false, "")
}

func (s *pullListenerSupervisor) run(ctx context.Context) {
	backoff := s.retryMin
	for {
		wait := s.check
		if s.srv == nil {
			wait = backoff
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			if s.srv != nil {
				s.cancel()
			}
			return
		case <-timer.C:
		}
		ip, err := s.ipFn(ctx)
		if err != nil {
			// No tailnet address at all: the bound IP is gone, so report
			// down. Other errors (an ambiguous second 100.x) keep the
			// current binding rather than flapping.
			if s.srv != nil && errors.Is(err, tsclient.ErrNoTailnetInterface) {
				fmt.Fprintf(os.Stderr, "agentcookie source --watch: tailnet address gone; pull listener down until Tailscale is back\n")
				s.down()
			}
			backoff = min(backoff*2, s.retryMax)
			continue
		}
		if s.srv != nil && ip == s.ip {
			backoff = s.retryMin
			continue
		}
		if s.srv != nil {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: tailnet IP changed %s -> %s; rebinding pull listener\n", s.ip, ip)
			s.down()
		}
		if err := s.bind(ctx, ip); err != nil {
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: %v; retrying\n", err)
			backoff = min(backoff*2, s.retryMax)
			continue
		}
		backoff = s.retryMin
	}
}
