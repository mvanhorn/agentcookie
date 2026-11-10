package destination

import (
	"context"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
)

func TestProbeRequiresVerifiedBrowserIncarnation(t *testing.T) {
	// A cached transport with the wrong incarnation must not execute any CDP
	// operation. The inert Browser would fail/panic if a target were requested.
	a := &ChromeApplier{browser: &chromedp.Browser{LostConnection: make(chan struct{})}, incarnation: "other-process"}
	specs := sinkpush.BuiltinVerifySpecs()
	for _, proof := range []string{"", "ws://127.0.0.1:19324/devtools/browser/owned"} {
		got := a.Probe(context.Background(), proof, specs)
		if len(got) != len(specs) || got[0].State != sinkpush.AuthUnknown {
			t.Fatalf("unverified process probed: %+v", got)
		}
	}
}
func TestEmptyProbeDoesNotConnect(t *testing.T) {
	a := NewChromeApplier("http://127.0.0.1:1")
	if got := a.Probe(context.Background(), "", nil); len(got) != 0 {
		t.Fatal(got)
	}
	if a.browser != nil {
		t.Fatal("empty probe connected")
	}
}
