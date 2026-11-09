package livecdp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

const testDefaultCtx cdp.BrowserContextID = "defaultctx"

func blankTab(id string) *target.Info {
	return &target.Info{TargetID: target.ID(id), Type: "page", URL: "about:blank", BrowserContextID: testDefaultCtx}
}

func realTab(id string) *target.Info {
	return &target.Info{TargetID: target.ID(id), Type: "page", URL: "https://github.com/", BrowserContextID: testDefaultCtx}
}

func TestStaleBlankTargets(t *testing.T) {
	stale1, stale2 := blankTab("stale1"), blankTab("stale2")
	navigated := realTab("navigated")
	agent := &target.Info{TargetID: "agent", Type: "page", URL: "about:blank", BrowserContextID: "agentctx"}
	// Incognito contexts are missing from Target.getBrowserContexts, so
	// only a positive default-context match may qualify a tab.
	incognito := &target.Info{TargetID: "incog", Type: "page", URL: "about:blank", BrowserContextID: "incognitoctx"}
	prerender := &target.Info{TargetID: "pre", Type: "page", Subtype: "prerender", URL: "about:blank", BrowserContextID: testDefaultCtx}
	worker := &target.Info{TargetID: "sw", Type: "service_worker", URL: "about:blank", BrowserContextID: testDefaultCtx}
	// A lane's idle tab parked at about:blank stays attached in both samples.
	parked := blankTab("parked")
	parked.Attached = true
	// window.open('') + document.write: real content under an about:blank URL.
	popup := blankTab("popup")
	popup.OpenerID = "real"

	first := []*target.Info{stale1, stale2, realTab("real"), blankTab("navigated"), agent, incognito, prerender, worker, parked, popup, nil}
	second := []*target.Info{stale1, stale2, realTab("real"), navigated, blankTab("fresh"), agent, incognito, prerender, worker, parked, popup, nil}

	var got []target.ID
	for _, info := range staleBlankTargets(first, second, testDefaultCtx) {
		got = append(got, info.TargetID)
	}
	want := []target.ID{"stale1", "stale2"}
	if !slices.Equal(got, want) {
		t.Fatalf("staleBlankTargets = %v, want %v", got, want)
	}
}

// fakeBrowser is a target list the close loop reads and closes from;
// onClose lets a test change the browser between closes, as another
// client would.
type fakeBrowser struct {
	targets []*target.Info
	closed  []target.ID
	lists   int
	infos   int
	onClose func(fb *fakeBrowser)
}

func (fb *fakeBrowser) list() ([]*target.Info, error) {
	fb.lists++
	out := make([]*target.Info, 0, len(fb.targets))
	for _, info := range fb.targets {
		c := *info
		out = append(out, &c)
	}
	return out, nil
}

func (fb *fakeBrowser) info(id target.ID) (*target.Info, error) {
	fb.infos++
	if info := fb.find(id); info != nil {
		c := *info
		return &c, nil
	}
	return nil, fmt.Errorf("no target with given id found")
}

func (fb *fakeBrowser) close(id target.ID) error {
	fb.closed = append(fb.closed, id)
	fb.remove(id)
	if fb.onClose != nil {
		fb.onClose(fb)
	}
	return nil
}

func (fb *fakeBrowser) remove(id target.ID) {
	fb.targets = slices.DeleteFunc(fb.targets, func(info *target.Info) bool { return info.TargetID == id })
}

func (fb *fakeBrowser) find(id target.ID) *target.Info {
	for _, info := range fb.targets {
		if info.TargetID == id {
			return info
		}
	}
	return nil
}

// candidates samples the sweepable tabs, as the two listings would.
func (fb *fakeBrowser) candidates() []*target.Info {
	all, _ := fb.list()
	fb.lists = 0
	return slices.DeleteFunc(all, func(info *target.Info) bool { return !isSweepableBlank(info, testDefaultCtx) })
}

func (fb *fakeBrowser) sweep(t *testing.T, candidates []*target.Info) []target.ID {
	t.Helper()
	if _, err := closeStaleBlanks(context.Background(), candidates, fb.list, fb.info, fb.close, testDefaultCtx); err != nil {
		t.Fatal(err)
	}
	return fb.closed
}

func noClosePause(t *testing.T) {
	prev := sweepClosePause
	sweepClosePause = 0
	t.Cleanup(func() { sweepClosePause = prev })
}

func TestCloseStaleBlanks_RevalidatesCandidates(t *testing.T) {
	noClosePause(t)
	fb := &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b"), blankTab("c"), blankTab("d"), blankTab("e"), realTab("real")}}
	candidates := fb.candidates()
	// After sampling, another client navigates b, attaches to c, and
	// closes d.
	fb.find("b").URL = "https://example.com/"
	fb.find("c").Attached = true
	fb.remove("d")
	if got := fb.sweep(t, candidates); !slices.Equal(got, []target.ID{"a"}) {
		t.Fatalf("closed %v, want only [a]: b, c, d changed after sampling and e is the spared last candidate", got)
	}
}

// TestCloseStaleBlanks_SparesTabChangedMidBatch: the listing a close is
// checked against can be up to sweepRelistEvery closes old. A tab another
// client navigates or attaches to after that listing must still be spared.
//
// Guard-revert: drop the per-close info check and this goes red.
func TestCloseStaleBlanks_SparesTabChangedMidBatch(t *testing.T) {
	noClosePause(t)
	fb := &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b"), blankTab("c"), blankTab("d"), realTab("real")}}
	// Right after a closes, inside the same batch: b is navigated, c is
	// attached. No relist happens before their turn.
	fb.onClose = func(fb *fakeBrowser) {
		if b := fb.find("b"); b != nil {
			b.URL = "https://example.com/"
		}
		if c := fb.find("c"); c != nil {
			c.Attached = true
		}
	}
	if got := fb.sweep(t, fb.candidates()); !slices.Equal(got, []target.ID{"a"}) {
		t.Fatalf("closed %v, want only [a]: b and c changed mid-batch and d is the spared last candidate", got)
	}
	if fb.lists != 1 {
		t.Fatalf("%d listings, want 1: the mid-batch change must be caught without a relist", fb.lists)
	}
}

func TestCloseStaleBlanks_KeepsAPage(t *testing.T) {
	noClosePause(t)
	fb := &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b"), realTab("real")}}
	// After a closes, another client closes the only real page; the
	// listing does not show it until the next relist.
	fb.onClose = func(fb *fakeBrowser) { fb.remove("real") }
	if got := fb.sweep(t, fb.candidates()); !slices.Equal(got, []target.ID{"a"}) {
		t.Fatalf("closed %v, want only [a]: b must survive as the last page", got)
	}

	// A prerender target is not a page that keeps a window open.
	prerender := &target.Info{TargetID: "pre", Type: "page", Subtype: "prerender", URL: "https://x.com/", BrowserContextID: testDefaultCtx}
	fb = &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b"), prerender}}
	if got := fb.sweep(t, fb.candidates()); len(got) != 1 {
		t.Fatalf("closed %v, want exactly 1 of 2 blanks", got)
	}
}

// TestCloseStaleBlanks_StopsWhenSparedCandidateIsGone covers the live page
// count, which the spare-last-candidate floor cannot: when another client
// closed the spared candidate before the listing, closing the rest could
// leave no page at all.
func TestCloseStaleBlanks_StopsWhenSparedCandidateIsGone(t *testing.T) {
	noClosePause(t)
	fb := &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b")}}
	candidates := fb.candidates()
	fb.remove("b")
	if got := fb.sweep(t, candidates); len(got) != 0 {
		t.Fatalf("closed %v, want none: a is the browser's only page", got)
	}

	// A prerender target does not count as the surviving page.
	prerender := &target.Info{TargetID: "pre", Type: "page", Subtype: "prerender", URL: "https://x.com/", BrowserContextID: testDefaultCtx}
	fb = &fakeBrowser{targets: []*target.Info{blankTab("a"), blankTab("b"), prerender}}
	candidates = fb.candidates()
	fb.remove("b")
	if got := fb.sweep(t, candidates); len(got) != 0 {
		t.Fatalf("closed %v, want none: a is the only page besides a prerender", got)
	}
}

// TestCloseStaleBlanks_ClearsLargePileWithFewListings guards the budget:
// a pile of thousands must not cost one full listing per close.
func TestCloseStaleBlanks_ClearsLargePileWithFewListings(t *testing.T) {
	noClosePause(t)
	const n = 3000
	targets := []*target.Info{realTab("real")}
	for i := range n {
		targets = append(targets, blankTab(fmt.Sprintf("blank%d", i)))
	}
	fb := &fakeBrowser{targets: targets}
	closed := fb.sweep(t, fb.candidates())
	if len(closed) != n-1 {
		t.Fatalf("closed %d of %d blanks, want %d (all but the spared last)", len(closed), n, n-1)
	}
	// A fixed bound, not one derived from sweepRelistEvery, so relisting
	// before every close fails it.
	const maxLists = 100
	if fb.lists > maxLists {
		t.Fatalf("%d target listings for %d closes, want at most %d", fb.lists, len(closed), maxLists)
	}
}

// launchSweepChrome starts a headless Chrome on its own loopback port for
// the live tests (never the shared 9223 browser) and returns its endpoint
// and a browser-level chromedp context on it.
func launchSweepChrome(t *testing.T, port int) (string, context.Context) {
	t.Helper()
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 to run live-Chrome tab tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	oc, err := LaunchOwnedChrome(ctx, "", t.TempDir(), port, true)
	if err != nil {
		t.Fatalf("LaunchOwnedChrome: %v", err)
	}
	t.Cleanup(func() { _ = oc.Close() })
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, oc.Endpoint)
	t.Cleanup(allocCancel)
	bctx, bcancel := chromedp.NewContext(allocCtx)
	t.Cleanup(bcancel)
	return oc.Endpoint, bctx
}

func pageTargets(t *testing.T, bctx context.Context) map[target.ID]string {
	t.Helper()
	infos, err := chromedp.Targets(bctx)
	if err != nil {
		t.Fatalf("list targets: %v", err)
	}
	pages := map[target.ID]string{}
	for _, info := range infos {
		if info.Type == "page" {
			pages[info.TargetID] = info.URL
		}
	}
	return pages
}

func createTab(t *testing.T, bctx context.Context, url string) target.ID {
	t.Helper()
	ex, err := browserExecutor(bctx)
	if err != nil {
		t.Fatal(err)
	}
	id, err := target.CreateTarget(url).Do(ex)
	if err != nil {
		t.Fatalf("create tab %s: %v", url, err)
	}
	return id
}

// TestAttachAndInject_LeavesNoTabs is the regression test for the sink
// leaking one about:blank tab per inject: repeated inject cycles against a
// debug-port Chrome create no page target at all and leave the page
// targets unchanged. Counting creations, not just survivors, matters: the
// old path created a tab per inject and closed it on cancel with a 1s
// budget, which a quiet test browser meets and a loaded real one did not.
func TestAttachAndInject_LeavesNoTabs(t *testing.T) {
	endpoint, bctx := launchSweepChrome(t, 9412)
	before := pageTargets(t, bctx)

	var mu sync.Mutex
	var created []string
	chromedp.ListenBrowser(bctx, func(ev any) {
		if e, ok := ev.(*target.EventTargetCreated); ok && e.TargetInfo.Type == "page" {
			mu.Lock()
			created = append(created, e.TargetInfo.URL)
			mu.Unlock()
		}
	})
	ex, err := browserExecutor(bctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.SetDiscoverTargets(true).Do(ex); err != nil {
		t.Fatalf("Target.setDiscoverTargets: %v", err)
	}
	// setDiscoverTargets replays existing targets as created; drop those.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	created = nil
	mu.Unlock()

	cookie := chrome.Cookie{HostKey: "example.com", Name: "ac_tabs", Value: "v", Path: "/", IsSecure: 1, SameSite: 1}
	for i := range 5 {
		start := time.Now()
		n, err := AttachAndInject(context.Background(), endpoint, []chrome.Cookie{cookie})
		if err != nil {
			t.Fatalf("inject cycle %d: %v", i, err)
		}
		if n < 1 {
			t.Fatalf("inject cycle %d: injected %d contexts, want >=1", i, n)
		}
		t.Logf("inject cycle %d: %s", i, time.Since(start))
	}
	// A leaked tab would be closing or open by now; give stragglers a beat.
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 0 {
		t.Errorf("5 injects created %d page target(s): %v", len(created), created)
	}
	if after := pageTargets(t, bctx); len(after) != len(before) {
		t.Fatalf("page targets before %d (%v), after 5 injects %d (%v)", len(before), before, len(after), after)
	}
}

func TestSweepStaleBlankTabs_Live(t *testing.T) {
	endpoint, bctx := launchSweepChrome(t, 9413)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>real</body></html>"))
	}))
	t.Cleanup(srv.Close)

	// Chrome's own launch tab is a stale blank too.
	var stale []target.ID
	for id, url := range pageTargets(t, bctx) {
		if url == "about:blank" {
			stale = append(stale, id)
		}
	}
	stale = append(stale, createTab(t, bctx, "about:blank"), createTab(t, bctx, "about:blank"), createTab(t, bctx, "about:blank"))
	real := createTab(t, bctx, srv.URL)

	const minAge = 3 * time.Second
	type result struct {
		closed int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		n, err := SweepStaleBlankTabs(context.Background(), endpoint, minAge)
		done <- result{n, err}
	}()
	// Opened after the sweep's first listing, so younger than minAge.
	time.Sleep(minAge / 2)
	fresh := createTab(t, bctx, "about:blank")

	res := <-done
	if res.err != nil {
		t.Fatalf("SweepStaleBlankTabs: %v", res.err)
	}
	time.Sleep(500 * time.Millisecond)
	pages := pageTargets(t, bctx)
	survivors := 0
	for _, id := range stale {
		if _, ok := pages[id]; ok {
			survivors++
		}
	}
	// The sweep spares its last candidate, so exactly one stale blank stays.
	if survivors != 1 || res.closed != len(stale)-1 {
		t.Errorf("closed %d, %d of %d stale blanks survived; want %d closed, 1 survivor", res.closed, survivors, len(stale), len(stale)-1)
	}
	if _, ok := pages[real]; !ok {
		t.Errorf("real-URL tab %s was closed", real)
	}
	if _, ok := pages[fresh]; !ok {
		t.Errorf("fresh about:blank tab %s was closed", fresh)
	}
}
