package destination

import (
	"context"
	"encoding/json"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

// Probe checks only named, boolean VerifySpec predicates in a fresh hidden
// target. The caller must verify ownership and explicitly opt in to navigation.
// Neither errors nor page-derived values are included in results.
func (a *ChromeApplier) Probe(ctx context.Context, verifiedWebSocket string, specs []sinkpush.VerifySpec) []sinkpush.VerifyResult {
	out := make([]sinkpush.VerifyResult, 0, len(specs))
	for _, spec := range specs {
		out = append(out, a.probeOne(ctx, verifiedWebSocket, spec))
	}
	return out
}
func (a *ChromeApplier) probeOne(ctx context.Context, verifiedWebSocket string, spec sinkpush.VerifySpec) sinkpush.VerifyResult {
	result := sinkpush.VerifyResult{Host: spec.Host, State: sinkpush.AuthUnknown, Detail: "probe unavailable or timed out"}
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	b, incarnation, err := a.connect(ctx)
	if err != nil || verifiedWebSocket == "" || incarnation != digest([]byte(verifiedWebSocket)) {
		return result
	}
	browser := cdp.WithExecutor(ctx, b)
	id, err := target.CreateTarget("about:blank").WithBackground(true).WithHidden(true).Do(browser)
	if err != nil {
		return result
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(browser), 3*time.Second)
		defer cancel()
		_ = target.CloseTarget(id).Do(cleanup)
	}()
	root, cancelRoot := chromedp.NewContext(ctx)
	defer cancelRoot()
	chromedp.FromContext(root).Browser = b
	tab, cancelTab := chromedp.NewContext(root, chromedp.WithTargetID(id))
	defer cancelTab()
	if err = chromedp.Run(tab, chromedp.Navigate(spec.URL)); err != nil {
		return result
	}
	err = chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		tree, e := page.GetFrameTree().Do(ctx)
		if e != nil {
			return e
		}
		world, e := page.CreateIsolatedWorld(tree.Frame.ID).WithWorldName("agentcookie-auth-probe").Do(ctx)
		if e != nil {
			return e
		}
		for {
			obj, exception, e := runtime.Evaluate(spec.BooleanScript()).WithContextID(world).WithReturnByValue(true).Do(ctx)
			if e != nil {
				return e
			}
			if exception != nil {
				return context.Canceled
			}
			var encoded string
			if json.Unmarshal(obj.Value, &encoded) == nil {
				var flags struct {
					Ready  bool `json:"ready"`
					Authed bool `json:"authed"`
				}
				if json.Unmarshal([]byte(encoded), &flags) == nil && flags.Ready {
					result.Detail = ""
					result.State = sinkpush.AuthNo
					if flags.Authed {
						result.State = sinkpush.AuthYes
					}
					return nil
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}))
	if err != nil {
		result.State = sinkpush.AuthUnknown
	}
	return result
}
