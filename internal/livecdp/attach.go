package livecdp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// DefaultPollInterval is how often the Syncer scans for new browser
// contexts to inject. A connector like browser-use creates its own
// context on connect; the poll catches it. Kept short so a fresh context
// gets cookies before the agent's second navigation. Injection only fires
// for not-yet-seen contexts, so the steady-state cost is one cheap
// Target.getTargets call per tick.
const DefaultPollInterval = 600 * time.Millisecond

// CookieProvider returns the current decrypted, filtered cookie set to
// inject. It is called fresh each sync so the loop always injects current
// values (the source pipeline owns reading/decrypt/blocklist/DBSC).
type CookieProvider func() ([]chrome.Cookie, error)

// Syncer keeps a live browser's contexts injected with the user's cookies.
// It solves the isolated-context problem: a connector (browser-use,
// agent-browser) opens its own browser context, so a one-time browser-level
// cookie write never reaches the agent's pages. The Syncer injects into
// every context as it appears (poll) and re-injects all contexts on demand
// (ReinjectAll, used by the source-change watch loop).
//
// Injection uses Storage.setCookies addressed by browserContextId via the
// browser session -- it writes straight into a context's cookie store
// without attaching to or disturbing any tab the agent is driving.
type Syncer struct {
	browserCtx context.Context
	provider   CookieProvider
	pollEvery  time.Duration
	log        func(format string, args ...any)

	mu   sync.Mutex
	seen map[cdp.BrowserContextID]bool
}

// NewSyncer builds a Syncer bound to an already-connected chromedp browser
// context. log may be nil.
func NewSyncer(browserCtx context.Context, provider CookieProvider, log func(string, ...any)) *Syncer {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Syncer{
		browserCtx: browserCtx,
		provider:   provider,
		pollEvery:  DefaultPollInterval,
		log:        log,
		seen:       map[cdp.BrowserContextID]bool{},
	}
}

// Run injects into existing contexts immediately, then polls for new
// contexts until ctx is cancelled. Returns ctx.Err() on shutdown.
func (s *Syncer) Run(ctx context.Context) error {
	if _, err := s.syncNewContexts(); err != nil {
		s.log("livecdp: initial sync: %v", err)
	}
	t := time.NewTicker(s.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if n, err := s.syncNewContexts(); err != nil {
				s.log("livecdp: poll sync: %v", err)
			} else if n > 0 {
				s.log("livecdp: injected %d new context(s)", n)
			}
		}
	}
}

// ReinjectAll re-injects current cookies into ALL live contexts, including
// ones already seen. The source-change watch loop (U4) calls this so a
// cookie the user just refreshed propagates into running agent contexts.
func (s *Syncer) ReinjectAll() (int, error) {
	cookies, err := s.provider()
	if err != nil {
		return 0, fmt.Errorf("livecdp: provider: %w", err)
	}
	return InjectAllContexts(s.browserCtx, cookies)
}

// syncNewContexts injects only into browser contexts not yet seen.
func (s *Syncer) syncNewContexts() (int, error) {
	ids, explicit, err := injectableContexts(s.browserCtx)
	if err != nil {
		return 0, err
	}
	var cookies []chrome.Cookie
	loaded := false
	n := 0
	for _, id := range ids {
		s.mu.Lock()
		already := s.seen[id]
		s.mu.Unlock()
		if already {
			continue
		}
		if !loaded {
			cookies, err = s.provider()
			if err != nil {
				return n, fmt.Errorf("livecdp: provider: %w", err)
			}
			loaded = true
		}
		if err := injectIntoContext(s.browserCtx, id, explicit[id], cookies); err != nil {
			s.log("livecdp: inject context %q: %v", id, err)
			continue
		}
		s.mu.Lock()
		s.seen[id] = true
		s.mu.Unlock()
		n++
	}
	return n, nil
}

// InjectAllContexts injects cookies into every injectable browser context in
// the connected browser, once per unique BrowserContextID. Because
// Storage.setCookies is addressed by browserContextId, this reaches contexts
// a connector created for itself -- the fix for the isolated-context failure
// where a browser-level write never reached the agent's pages.
func InjectAllContexts(browserCtx context.Context, cookies []chrome.Cookie) (int, error) {
	ids, explicit, err := injectableContexts(browserCtx)
	if err != nil {
		return 0, err
	}
	n := 0
	var firstErr error
	for _, id := range ids {
		if err := injectIntoContext(browserCtx, id, explicit[id], cookies); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		n++
	}
	return n, firstErr
}

// injectableContexts returns the unique BrowserContextIDs that own at least
// one injectable page target, plus the set of those that are EXPLICIT
// (created via Target.createBrowserContext -- e.g. browser-use's own
// context). Explicit contexts must be addressed by browserContextId in
// Storage.setCookies; the default context is NOT addressable by id (Chrome
// rejects its id with -32602) and must be set with the param omitted.
func injectableContexts(browserCtx context.Context) ([]cdp.BrowserContextID, map[cdp.BrowserContextID]bool, error) {
	infos, err := chromedp.Targets(browserCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("livecdp: list targets: %w", err)
	}
	explicit, err := explicitContextSet(browserCtx)
	if err != nil {
		// Degrade gracefully: treat all as default (omit the id). Worst
		// case an explicit context misses; logged by the caller.
		explicit = map[cdp.BrowserContextID]bool{}
	}
	seen := map[cdp.BrowserContextID]bool{}
	var ids []cdp.BrowserContextID
	for _, info := range infos {
		if !shouldInjectTarget(info) || seen[info.BrowserContextID] {
			continue
		}
		seen[info.BrowserContextID] = true
		ids = append(ids, info.BrowserContextID)
	}
	return ids, explicit, nil
}

// explicitContextSet returns the browser contexts created via
// Target.createBrowserContext (the default context is not included).
func explicitContextSet(browserCtx context.Context) (map[cdp.BrowserContextID]bool, error) {
	var ids []cdp.BrowserContextID
	err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		got, _, e := target.GetBrowserContexts().Do(bctx)
		ids = got
		return e
	}))
	if err != nil {
		return nil, err
	}
	set := make(map[cdp.BrowserContextID]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// injectIntoContext writes cookies into one browser context's cookie store
// via Storage.setCookies on the browser session. useID controls whether the
// browserContextId param is sent: true for an explicit (createBrowserContext)
// context, false for the default context (which Chrome rejects when addressed
// by id). This never attaches to a page target, so it cannot close or disturb
// a tab the agent is driving.
func injectIntoContext(browserCtx context.Context, ctxID cdp.BrowserContextID, useID bool, cookies []chrome.Cookie) error {
	params := BuildCookieParams(cookies)
	if len(params) == 0 {
		return nil
	}
	return chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		bctx := cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser)
		sc := storage.SetCookies(params)
		if useID {
			sc = sc.WithBrowserContextID(ctxID)
		}
		if err := sc.Do(bctx); err != nil {
			return fmt.Errorf("Storage.setCookies (%d cookies, ctx=%q useID=%v): %w", len(params), ctxID, useID, err)
		}
		return nil
	}))
}

// shouldInjectTarget reports whether a target should receive cookies: real
// page targets only, excluding Chrome-internal and extension surfaces and
// prerender subframes. about:blank pages qualify -- they belong to a real
// context whose cookie store the agent's pages will read.
func shouldInjectTarget(info *target.Info) bool {
	if info == nil || info.Type != "page" || info.Subtype == "prerender" {
		return false
	}
	for _, p := range []string{"chrome://", "devtools://", "chrome-extension://", "chrome-untrusted://"} {
		if strings.HasPrefix(info.URL, p) {
			return false
		}
	}
	return true
}
