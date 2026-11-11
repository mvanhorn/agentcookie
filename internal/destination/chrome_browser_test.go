package destination

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

// TestChromeDestinationLifecycle is a real browser gate, not a fake CDP test.
// Only throwaway HOME/profile/state and mock browser credentials are used.
func TestChromeDestinationLifecycle(t *testing.T) {
	if os.Getenv("AGENTCOOKIE_LIVE_CDP_TEST") == "" && os.Getenv("AGENTCOOKIE_E2E_REQUIRED") == "" {
		t.Skip("set AGENTCOOKIE_LIVE_CDP_TEST=1 or AGENTCOOKIE_E2E_REQUIRED=1")
	}
	binary := os.Getenv("AGENTCOOKIE_CHROME_BINARY")
	var err error
	if binary == "" {
		binary, err = livecdp.FindChrome()
	}
	if err != nil {
		if os.Getenv("AGENTCOOKIE_E2E_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("TMPDIR", home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	headed := false
	m := NewManaged(config.DestinationRef{Name: "fixture", Kind: "live_chrome", Chrome: config.DestinationChrome{UserDataDir: filepath.Join(home, "chrome"), Port: port, Headed: &headed, Path: binary, Start: "on_demand", Relaunch: "after_crash"}})
	m.MockCredentials = true
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stateDir := filepath.Join(home, "destination")
	e, err := Open(stateDir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	cookie := func(name, value string) Item {
		item, err := cookieItem(chrome.Cookie{HostKey: "127.0.0.1", Name: name, Value: value, Path: "/", SameSite: -1, HasExpires: 1, IsPersistent: 1, ExpiresUTC: (time.Now().Add(time.Hour).Unix() + 11644473600) * 1000000})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	current := cookie("session", "one")
	st, err := e.Accept(ctx, "fixture", snap(1, current))
	if err != nil || !st.Ready {
		t.Fatalf("initial restore: %+v %v", st, err)
	}
	b, _, err := m.Chrome.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := target.GetTargets().Do(cdp.WithExecutor(ctx, b))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range targets {
		if info.Type == "page" {
			t.Fatalf("visible page opened before launcher: %s", info.URL)
		}
	}
	requests := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("session")
		value := ""
		if c != nil {
			value = c.Value
		}
		requests <- value
		fmt.Fprintln(w, "fixture")
	}))
	t.Cleanup(server.Close)
	assertRequest := func(want string) {
		t.Helper()
		b, _, err := m.Chrome.connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		exec := cdp.WithExecutor(ctx, b)
		id, err := target.CreateTarget(server.URL).WithHidden(true).Do(exec)
		if err != nil {
			t.Fatal(err)
		}
		defer target.CloseTarget(id).Do(exec)
		select {
		case value := <-requests:
			if value != want {
				t.Fatalf("wire cookie %q want %q", value, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fixture did not receive browser request")
		}
	}
	assertRequest("one")
	current = cookie("session", "two")
	st, err = e.Accept(ctx, "fixture", snap(2, current))
	if err != nil || !st.Ready {
		t.Fatalf("rotation: %+v %v", st, err)
	}
	assertRequest("two")
	// An isolated connector context gets the complete desired snapshot.
	private, err := target.CreateBrowserContext().Do(cdp.WithExecutor(ctx, b))
	if err != nil {
		t.Fatal(err)
	}
	st, err = e.Reconcile(ctx, "fixture")
	if err != nil || !st.Ready {
		t.Fatal(err)
	}
	contexts, _ := m.Contexts(ctx)
	for _, c := range contexts {
		h, ok, err := m.Read(ctx, c, current)
		if err != nil || !ok || h != current.Hash {
			t.Fatalf("context not restored: %s %v", c.ID, err)
		}
	}
	if err := target.DisposeBrowserContext(private).Do(cdp.WithExecutor(ctx, b)); err != nil {
		t.Fatal(err)
	}
	// Closing the owned process holds the next desired generation until explicit
	// reopen, rather than overwriting a real profile or launching a visible tab.
	closeChrome := func() {
		t.Helper()
		b, _, err := m.Chrome.connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = browser.Close().Do(cdp.WithExecutor(ctx, b))
		deadline := time.Now().Add(5 * time.Second)
		for {
			exited, _ := m.owned.Exited()
			if exited {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("owned browser failed to close")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	closeChrome()
	current = cookie("session", "three")
	st, err = e.Accept(ctx, "fixture", snap(3, current))
	if err == nil || st.Ready || st.Accepted != 3 {
		t.Fatalf("closed destination not held: %+v %v", st, err)
	}
	if err = m.Reopen(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = e.Reconcile(ctx, "fixture")
	if err != nil || !st.Ready {
		t.Fatalf("relaunch restore: %+v %v", st, err)
	}
	assertRequest("three")
	contexts, _ = m.Contexts(ctx)
	local := cookie("sink_only", "keep")
	if err = m.Set(ctx, contexts[0], local); err != nil {
		t.Fatal(err)
	}
	st, err = e.Accept(ctx, "fixture", snap(4))
	if err != nil || !st.Ready {
		t.Fatal(err)
	}
	assertRequest("")
	if _, ok, err := m.Read(ctx, contexts[0], local); err != nil || !ok {
		t.Fatal("sink-only cookie removed")
	}
	e.Fault = func(stage string) error {
		if stage == "after-write" {
			return errors.New("injected crash after browser write")
		}
		return nil
	}
	if _, err = e.Accept(ctx, "fixture", snap(5, cookie("session", "healed"))); err == nil {
		t.Fatal("crash not injected")
	}
	e, err = Open(stateDir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	st, err = e.Reconcile(ctx, "fixture")
	if err != nil || !st.Ready {
		t.Fatalf("crash recovery: %+v %v", st, err)
	}
	assertRequest("healed")
	// Persistent default-profile cookies survive process exit. Logout while
	// closed must revoke that prior ownership before the first visible window.
	before, _ := m.Contexts(ctx)
	closeChrome()
	st, err = e.Accept(ctx, "fixture", snap(6))
	if err == nil || st.Ready || st.Accepted != 6 {
		t.Fatalf("closed logout not held: %+v %v", st, err)
	}
	e, err = Open(stateDir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Reopen(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = e.Reconcile(ctx, "fixture")
	if err != nil || !st.Ready {
		t.Fatalf("persistent logout restore: %+v %v", st, err)
	}
	after, _ := m.Contexts(ctx)
	if before[0].Incarnation == after[0].Incarnation {
		t.Fatal("browser did not restart")
	}
	b, _, err = m.Chrome.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targets, err = target.GetTargets().Do(cdp.WithExecutor(ctx, b))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range targets {
		if info.Type == "page" {
			t.Fatal("visible page opened before persistent logout restore")
		}
	}
	assertRequest("")
	if _, ok, err := m.Read(ctx, after[0], local); err != nil || !ok {
		t.Fatal("persistent sink-only cookie lost across logout/restart")
	}
}
