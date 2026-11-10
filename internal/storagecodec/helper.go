package storagecodec

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Helper owns no browser process. Each operation uses a fresh synthetic target
// in the requested context. It never navigates an existing user tab.
// Callers must journal every affected item before invoking a mutation.
type Helper struct{ Browser context.Context }

func ValidOrigin(origin string) error {
	u, err := url.Parse(origin)
	if len(origin) > 2048 || err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(origin, "\x00\r\n") {
		return errors.New("invalid storage origin")
	}
	return nil
}

// Run invokes a named shared primitive using structured CDP arguments. A fresh
// random title and the actual target context are checked before code runs.
func (h *Helper) Run(ctx context.Context, contextID cdp.BrowserContextID, origin, operation string, payload map[string]any, out any) error {
	if err := ValidOrigin(origin); err != nil {
		return err
	}
	switch operation {
	case "localStorage", "indexedDB", "readLocalStorage", "readIndexedDB", "readIndexedDBBatch", "readSchema":
	default:
		return errors.New("unknown storage operation")
	}
	if h == nil || h.Browser == nil {
		return errors.New("missing helper browser")
	}
	c := chromedp.FromContext(h.Browser)
	if c == nil || c.Browser == nil {
		return errors.New("browser not connected")
	}
	browser := cdp.WithExecutor(ctx, c.Browser)
	id, err := target.CreateTarget("about:blank").WithBrowserContextID(contextID).WithBackground(true).WithHidden(true).Do(browser)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(browser), 3*time.Second)
		defer cancel()
		_ = target.CloseTarget(id).Do(cleanup)
	}()
	info, err := target.GetTargetInfo().WithTargetID(id).Do(browser)
	if err != nil {
		return err
	}
	if contextID != "" {
		if info.BrowserContextID != contextID {
			return errors.New("helper context mismatch")
		}
	} else {
		// Chrome reports an opaque ID for its default context but rejects that ID
		// as a createTarget parameter. Prove it is not an explicit context instead.
		explicit, _, err := target.GetBrowserContexts().Do(browser)
		if err != nil {
			return err
		}
		if slices.Contains(explicit, info.BrowserContextID) {
			return errors.New("helper unexpectedly in explicit context")
		}
	}
	page, cancel := chromedp.NewContext(h.Browser, chromedp.WithTargetID(id))
	defer cancel()
	page, stop := context.WithCancel(page)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-page.Done():
		}
	}()
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(random[:])
	synthetic := origin + "/.agentcookie-apply/" + nonce
	interceptErrors := make(chan error, 1)
	chromedp.ListenTarget(page, func(event any) {
		e, ok := event.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		go func() {
			exec := cdp.WithExecutor(page, chromedp.FromContext(page).Target)
			var err error
			if e.Request.URL == synthetic && e.ResourceType == network.ResourceTypeDocument {
				err = fetch.FulfillRequest(e.RequestID, 200).WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=utf-8"}, {Name: "Content-Security-Policy", Value: "default-src 'none'"}, {Name: "Cache-Control", Value: "no-store"}}).WithBody(base64.StdEncoding.EncodeToString([]byte("<!doctype html><title>agentcookie " + nonce + "</title>"))).Do(exec)
			} else {
				err = fetch.FailRequest(e.RequestID, network.ErrorReasonBlockedByClient).Do(exec)
			}
			if err != nil {
				select {
				case interceptErrors <- err:
				default:
				}
			}
		}()
	})
	if err := chromedp.Run(page, network.Enable(), network.SetBypassServiceWorker(true), fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}})); err != nil {
		return err
	}
	quoted, _ := json.Marshal(synthetic)
	if err := chromedp.Run(page, chromedp.Evaluate("location.href = "+string(quoted), nil)); err != nil {
		return err
	}
	originJSON, _ := json.Marshal(origin)
	titleJSON, _ := json.Marshal("agentcookie " + nonce)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		var ready bool
		if err := chromedp.Run(page, chromedp.Evaluate("location.origin === "+string(originJSON)+" && document.title === "+string(titleJSON), &ready)); err == nil && ready {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-interceptErrors:
			return fmt.Errorf("helper intercept: %w", err)
		case <-deadline.C:
			return errors.New("helper nonce timeout")
		case <-tick.C:
		}
	}
	if err := chromedp.Run(page, chromedp.Evaluate(CodecJS+"\n"+ApplierJS, nil)); err != nil {
		return err
	}
	args := make(map[string]any, len(payload)+2)
	maps.Copy(args, payload)
	args["origin"] = origin
	args["nonce"] = nonce
	arg, err := json.Marshal(args)
	if err != nil {
		return err
	}
	if len(arg) > 128<<20 {
		return errors.New("storage surface bytes limit")
	}
	op, _ := json.Marshal(operation)
	return chromedp.Run(page, chromedp.ActionFunc(func(ctx context.Context) error {
		global, exception, err := runtime.Evaluate("globalThis").Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("helper global: %s", exception.Text)
		}
		value, exception, err := runtime.CallFunctionOn("function(op, arg) { return globalThis.AgentCookieStorageApplier[op](arg); }").WithObjectID(global.ObjectID).WithArguments([]*runtime.CallArgument{{Value: op}, {Value: arg}}).WithAwaitPromise(true).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("storage apply: %s", exception.Text)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(value.Value, out)
	}))
}
