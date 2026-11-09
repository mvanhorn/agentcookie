package livecdp

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// DefaultSweepMinAge is how long an about:blank tab must stay blank before
// SweepStaleBlankTabs closes it. The attached Chrome is shared: another
// client may have just opened a blank tab it is about to navigate.
const DefaultSweepMinAge = 60 * time.Second

// SweepStaleBlankTabs makes one sweep pass over the Chrome at endpoint,
// closing the about:blank tabs earlier attach-and-inject runs leaked (one
// per inject). CDP reports no target creation time, so age is observed: it
// lists the targets, waits minAge, lists again, and picks tabs that were
// sweepable both times. Each pick is checked again against a listing taken
// at most sweepRelistEvery closes earlier. It never closes a page with a
// real URL, a tab a client is attached to, a popup a page opened, a
// non-page target (service worker, extension, browser UI), a tab outside
// the default browser context (an agent's own context, incognito), or the
// last page, which on Linux would quit the browser. It returns the number
// of tabs closed; a caller with a large pile runs passes until one closes
// none.
func SweepStaleBlankTabs(ctx context.Context, endpoint string, minAge time.Duration) (int, error) {
	if endpoint == "" {
		endpoint = DefaultCDPEndpoint
	}
	if err := waitForCDPEndpoint(ctx, endpoint, 5*time.Second); err != nil {
		return 0, fmt.Errorf("livecdp: sweep: cannot reach CDP endpoint %s: %w", endpoint, err)
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, endpoint+"/json/version")
	defer allocCancel()
	browserCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	first, err := chromedp.Targets(browserCtx)
	if err != nil {
		return 0, fmt.Errorf("livecdp: sweep: list targets: %w", err)
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(minAge):
	}
	second, err := chromedp.Targets(browserCtx)
	if err != nil {
		return 0, fmt.Errorf("livecdp: sweep: list targets: %w", err)
	}

	bctx, err := browserExecutor(browserCtx)
	if err != nil {
		return 0, err
	}
	// The default context is identified by id, not by absence from the
	// explicit list: incognito contexts are in neither.
	_, defaultID, err := target.GetBrowserContexts().Do(bctx)
	if err != nil {
		return 0, fmt.Errorf("livecdp: sweep: list browser contexts: %w", err)
	}
	if defaultID == "" {
		// This Chrome does not report its default context, so no blank
		// tab can be placed safely; close nothing.
		return 0, nil
	}

	list := func() ([]*target.Info, error) { return chromedp.Targets(browserCtx) }
	info := func(id target.ID) (*target.Info, error) { return target.GetTargetInfo().WithTargetID(id).Do(bctx) }
	closeTarget := func(id target.ID) error { return target.CloseTarget(id).Do(bctx) }
	return closeStaleBlanks(ctx, staleBlankTargets(first, second, defaultID), list, info, closeTarget, defaultID)
}

// staleBlankTargets picks the sweep candidates from two listings taken at
// least minAge apart: blank tabs in the default context that were already
// blank in the first listing. It returns each candidate's info from the
// second listing, so a later change can be seen.
func staleBlankTargets(first, second []*target.Info, defaultID cdp.BrowserContextID) []*target.Info {
	wasBlank := map[target.ID]bool{}
	for _, info := range first {
		if isSweepableBlank(info, defaultID) {
			wasBlank[info.TargetID] = true
		}
	}
	var stale []*target.Info
	for _, info := range second {
		if isSweepableBlank(info, defaultID) && wasBlank[info.TargetID] {
			stale = append(stale, info)
		}
	}
	return stale
}

// sweepRelistEvery is how many closes the sweep makes per fresh target
// listing. Listing before every close was O(N^2) against a browser holding
// thousands of leaked tabs and outran the sweep's budget.
const sweepRelistEvery = 50

// sweepClosePause spaces closes so the sweep does not crowd out the sink's
// own CDP injects right after a restart. Tests set it to zero.
var sweepClosePause = 10 * time.Millisecond

// closeStaleBlanks closes the candidates one at a time. It lists the targets
// again before the first close and after every sweepRelistEvery closes, and
// checks each candidate against the latest listing: one that is gone or is
// no longer a sweepable blank tab is skipped. That listing can be up to
// sweepRelistEvery closes old, so immediately before each close it also
// re-reads the candidate itself (info, one Target.getTargetInfo) and skips
// it if another client has since navigated, attached to, or closed it.
// Tabs it closed since the listing are counted out of the page total, and
// it stops once only one page would remain. Another client may have closed
// the real pages since that listing; the last candidate is therefore never
// closed, so a page always survives the pass.
func closeStaleBlanks(ctx context.Context, candidates []*target.Info, list func() ([]*target.Info, error), info func(target.ID) (*target.Info, error), closeTarget func(target.ID) error, defaultID cdp.BrowserContextID) (int, error) {
	closed := 0
	var firstErr error
	var latest map[target.ID]*target.Info
	pages, sinceList := 0, sweepRelistEvery
	if len(candidates) > 0 {
		candidates = candidates[:len(candidates)-1]
	}
	for _, sampled := range candidates {
		if sinceList >= sweepRelistEvery {
			live, err := list()
			if err != nil {
				return closed, fmt.Errorf("livecdp: sweep: list targets: %w", err)
			}
			latest = make(map[target.ID]*target.Info, len(live))
			for _, info := range live {
				if info != nil {
					latest[info.TargetID] = info
				}
			}
			pages, sinceList = livePages(live), 0
		}
		if pages <= 1 {
			break
		}
		cur := latest[sampled.TargetID]
		if !isSweepableBlank(cur, defaultID) {
			continue
		}
		if closed > 0 && sweepClosePause > 0 {
			select {
			case <-ctx.Done():
				return closed, ctx.Err()
			case <-time.After(sweepClosePause):
			}
		}
		// Last look, after the pause: a tab that errors here is gone.
		if fresh, err := info(cur.TargetID); err != nil || !isSweepableBlank(fresh, defaultID) {
			continue
		}
		if err := closeTarget(cur.TargetID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("livecdp: sweep: close target %s: %w", cur.TargetID, err)
			}
			continue
		}
		delete(latest, cur.TargetID)
		pages--
		sinceList++
		closed++
	}
	return closed, firstErr
}

// livePages counts the page targets a user could see; a prerender target is
// not one, so it does not keep the browser's last window open.
func livePages(infos []*target.Info) int {
	n := 0
	for _, info := range infos {
		if info != nil && info.Type == "page" && info.Subtype != "prerender" {
			n++
		}
	}
	return n
}

// isSweepableBlank reports whether info looks like a leaked inject tab: a
// plain about:blank page in the default browser context that no client is
// attached to and no page opened. It is checked in every observation, so a
// tab attached in any of them is spared.
//
// Attached tabs are spared even if they were leaked: a Puppeteer or
// Playwright client auto-attaches to every page, including an idle tab it
// parked at about:blank. While such a client is connected the sweep closes
// nothing; the leak itself is fixed, and later quiet sink starts drain the
// pile. A tab with an opener is a page-made popup (window.open(”) then
// document.write), which keeps the about:blank URL while showing real
// content; the leaked tabs came from Target.createTarget and have none.
func isSweepableBlank(info *target.Info, defaultID cdp.BrowserContextID) bool {
	return info != nil && info.Type == "page" && info.Subtype == "" &&
		info.URL == "about:blank" && info.BrowserContextID == defaultID &&
		!info.Attached && info.OpenerID == ""
}
