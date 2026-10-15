package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// liveCDPInject injects cookies into the running Chrome at a CDP endpoint.
// Tests replace it so they never dial Chrome.
var liveCDPInject = livecdp.AttachAndInject

// liveCDPReinjectInterval is how often a live CDP sink re-injects the
// cookies from its last /sync. It matches the source's baseline tick, which
// re-pushed every 30s and so re-injected as a side effect.
const liveCDPReinjectInterval = 30 * time.Second

// liveCDPReinjector re-injects the cookies from the last successful /sync
// into the running Chrome on a timer. The source skips a sink
// that already holds the current payload and re-sends only every 10
// minutes, so without this a browser context an agent opened since the last
// /sync, or a Chrome that restarted, would stay logged out until then. The
// cookies are held in memory only; after a sink restart the source's next
// change or heartbeat supplies them.
type liveCDPReinjector struct {
	cfg *config.SinkConfig
	log io.Writer

	// stateWriter, sinkState, and stateMu are the /sync path's status
	// state, so each tick's outcome reaches `agentcookie status`.
	stateWriter *state.Writer
	sinkState   *state.SinkState
	stateMu     *sync.Mutex

	mu       sync.Mutex
	cookies  []chrome.Cookie
	applying int
	// heldSource and heldSeq identify the envelope cookies came from.
	heldSource string
	heldSeq    int64

	// injectMu covers a tick's snapshot and inject, so a /sync that starts
	// mid-tick waits for it and its newer cookies are injected last.
	injectMu sync.Mutex

	// failing is the last re-inject outcome, so a persistent failure logs
	// once and recovery logs once instead of every tick. Only the ticker
	// goroutine touches it.
	failing bool
}

func newLiveCDPReinjector(cfg *config.SinkConfig, stateWriter *state.Writer, sinkState *state.SinkState, stateMu *sync.Mutex) *liveCDPReinjector {
	return &liveCDPReinjector{cfg: cfg, log: os.Stderr, stateWriter: stateWriter, sinkState: sinkState, stateMu: stateMu}
}

// beginApply marks a /sync apply in progress; ticks skip until endApply.
// It first waits for an in-flight tick to finish injecting.
func (r *liveCDPReinjector) beginApply() {
	r.injectMu.Lock()
	defer r.injectMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applying++
}

// endApply ends a /sync apply and, when it succeeded with live CDP on,
// holds its cookie set for the next ticks. Concurrent applies can finish
// out of order, so an older envelope from the held source never replaces
// a newer one; a different source replaces it (last writer wins).
func (r *liveCDPReinjector) endApply(res envelopeApplyResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applying--
	if res.Status != http.StatusOK || !r.cfg.LiveCDP.Enabled {
		return
	}
	if r.cookies != nil && res.sourceHostname == r.heldSource && res.sequence < r.heldSeq {
		return
	}
	r.cookies = res.liveCDPCookies
	r.heldSource = res.sourceHostname
	r.heldSeq = res.sequence
}

// tick re-injects the held cookies once. It reports whether it called the
// injector: it skips while no set is held or a /sync apply is running.
func (r *liveCDPReinjector) tick(ctx context.Context) bool {
	r.injectMu.Lock()
	defer r.injectMu.Unlock()
	r.mu.Lock()
	cookies, busy := r.cookies, r.applying > 0
	r.mu.Unlock()
	if busy || len(cookies) == 0 || !r.cfg.LiveCDP.Enabled {
		return false
	}
	// Re-check the sink's current policy, as /sync would: a domain the
	// operator blocked since the last /sync must not keep reaching new
	// browser contexts until the source's next heartbeat.
	bl, err := loadFreshBlocklist()
	if err != nil {
		r.reportFailure(fmt.Errorf("load cookie policy: %w", err))
		return false
	}
	cookies, _ = protocol.NewBlocklistMatcherForSink(bl).Filter(cookies)
	if len(cookies) == 0 {
		return false
	}
	// Bound each attempt so a hung CDP call cannot stop the ticker for good.
	injectCtx, cancel := context.WithTimeout(ctx, liveCDPReinjectInterval)
	defer cancel()
	contexts, err := liveCDPInject(injectCtx, liveCDPEndpointFor(r.cfg), cookies)
	r.recordState(len(cookies), contexts, err)
	if err != nil {
		r.reportFailure(err)
	} else {
		r.reportRecovery(len(cookies), contexts)
	}
	return true
}

// recordState records a re-inject attempt's outcome in the sink status
// state, as the /sync path does.
func (r *liveCDPReinjector) recordState(cookies, contexts int, err error) {
	if r.sinkState == nil {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	recordLiveCDPInject(r.sinkState, liveCDPEndpointFor(r.cfg), cookies, contexts, err)
	if r.stateWriter != nil {
		_ = r.stateWriter.Save(r.sinkState)
	}
}

// reportFailure logs a failed re-inject once, until a recovery.
func (r *liveCDPReinjector) reportFailure(err error) {
	if r.failing {
		return
	}
	r.failing = true
	fmt.Fprintf(r.log, "agentcookie sink: live CDP re-inject failed at %s: %v (retrying every %s; logged again on recovery)\n", liveCDPEndpointFor(r.cfg), err, liveCDPReinjectInterval)
}

// reportRecovery logs the first successful re-inject after a failure.
func (r *liveCDPReinjector) reportRecovery(cookies, contexts int) {
	if !r.failing {
		return
	}
	r.failing = false
	fmt.Fprintf(r.log, "agentcookie sink: live CDP re-inject recovered: %d cookies into %d context(s) at %s\n", cookies, contexts, liveCDPEndpointFor(r.cfg))
}

// liveCDPEndpointFor is the sink's live CDP endpoint, or the default.
func liveCDPEndpointFor(cfg *config.SinkConfig) string {
	if cfg.LiveCDP.Endpoint != "" {
		return cfg.LiveCDP.Endpoint
	}
	return livecdp.DefaultCDPEndpoint
}

// run calls tick on every value from ticks until ctx ends.
func (r *liveCDPReinjector) run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			r.tick(ctx)
		}
	}
}

// startLiveCDPReinject starts the re-inject ticker when live CDP is on and
// the sink may modify Chrome (not --dry-run). It reports whether it started.
func startLiveCDPReinject(ctx context.Context, cfg *config.SinkConfig, r *liveCDPReinjector) bool {
	if !cfg.LiveCDP.Enabled || sinkDryRun {
		return false
	}
	ticker := time.NewTicker(liveCDPReinjectInterval)
	go func() {
		defer ticker.Stop()
		r.run(ctx, ticker.C)
	}()
	return true
}
