package destination

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

// Managed owns the lifecycle of a dedicated Chrome. A clean close stays closed
// under after_crash; snapshots still become durable desired state while closed.
type Managed struct {
	mu              sync.Mutex
	Config          config.DestinationRef
	Chrome          *ChromeApplier
	owned           *livecdp.OwnedChrome
	started         bool
	closed          bool
	MockCredentials bool // hermetic fixtures only, never inferred from HOME
}

func NewManaged(cfg config.DestinationRef) *Managed {
	endpoint := cfg.Endpoint
	if cfg.Kind == "live_chrome" {
		endpoint = fmt.Sprintf("http://127.0.0.1:%d", cfg.Chrome.Port)
	}
	return &Managed{Config: cfg, Chrome: NewChromeApplier(endpoint)}
}
func (m *Managed) Ensure(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("destination shut down")
	}
	if m.Config.Kind == "attach" {
		return nil
	}
	if m.owned != nil {
		exited, clean := m.owned.Exited()
		if !exited {
			return nil
		}
		m.Chrome.Close()
		if m.Config.Chrome.Relaunch == "never" || clean && m.Config.Chrome.Relaunch != "always" {
			return fmt.Errorf("owned Chrome closed; desired state held until explicitly reopened")
		}
		m.owned = nil
	}
	if m.started && m.Config.Chrome.Relaunch == "never" {
		return fmt.Errorf("owned Chrome relaunch disabled")
	}
	// Resolve symlinks in every existing parent before rejecting the real default
	// profile. Validation never opens browser databases.
	dir := m.Config.Chrome.UserDataDir
	parent := dir
	for {
		if resolved, e := filepath.EvalSymlinks(parent); e == nil {
			suffix, _ := filepath.Rel(parent, dir)
			dir = filepath.Join(resolved, suffix)
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			break
		}
		parent = next
	}
	home, _ := os.UserHomeDir()
	if config.DefaultProfileOverlap(dir, home) {
		return fmt.Errorf("refusing default browser profile or its ancestor")
	}

	if !m.started {
		prior, err := livecdp.ResumeOwnedChrome(ctx, dir, m.Config.Chrome.Port)
		if err != nil {
			return err
		}
		if prior != nil {
			m.owned = prior
			m.started = true
			return nil
		}
	}
	headed := m.Config.Chrome.Headed == nil || *m.Config.Chrome.Headed
	owned, err := livecdp.LaunchOwnedChromeOptions(ctx, m.Config.Chrome.Path, dir, m.Config.Chrome.Port, livecdp.LaunchOptions{Headless: !headed, NoStartupWindow: true, MockCredentials: m.MockCredentials})
	if err != nil {
		return err
	}
	m.owned = owned
	m.started = true
	return nil
}
func (m *Managed) Contexts(ctx context.Context) ([]Context, error) {
	if err := m.Ensure(ctx); err != nil {
		return nil, err
	}
	contexts, err := m.Chrome.Contexts(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Config.Kind == "live_chrome" && m.owned != nil {
		profile, err := ownedProfileIdentity(m.owned.UserDataDir)
		if err != nil {
			return nil, fmt.Errorf("identify owned default profile: %w", err)
		}
		for i := range contexts {
			if contexts[i].Default {
				contexts[i].OwnedProfile = profile
			}
		}
	}
	return contexts, nil
}
func (m *Managed) Read(ctx context.Context, c Context, i Item) (string, bool, error) {
	return m.Chrome.Read(ctx, c, i)
}
func (m *Managed) Set(ctx context.Context, c Context, i Item) error { return m.Chrome.Set(ctx, c, i) }
func (m *Managed) Delete(ctx context.Context, c Context, i Item) error {
	return m.Chrome.Delete(ctx, c, i)
}
func (m *Managed) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.Chrome.Close()
	if m.owned != nil {
		_ = m.owned.Close()
	}
}

// OpenWindow is called only after the default context is fully restored.
func (m *Managed) OpenWindow(ctx context.Context) error {
	b, _, err := m.Chrome.connect(ctx)
	if err != nil {
		return err
	}
	_, err = target.CreateTarget("about:blank").WithNewWindow(true).Do(cdp.WithExecutor(ctx, b))
	return err
}

// Run periodically restores new contexts and retries pending work without
// requiring a fresh source envelope. It never accepts older queued snapshots.
func (m *Managed) Run(ctx context.Context, e *Engine) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := map[string]time.Time{}
	var signature string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			sources := e.Sources()
			if len(sources) == 0 {
				continue
			}
			contexts, err := m.Contexts(ctx)
			var current strings.Builder
			if err == nil {
				for _, c := range contexts {
					current.WriteString(c.Key())
				}
			}
			changed := current.String() != signature
			signature = current.String()
			for _, source := range sources {
				st, _ := e.Status(source)
				cadence := 30 * time.Second
				if !st.Ready {
					cadence = 5 * time.Second
				}
				if changed || time.Since(last[source]) >= cadence {
					_, _ = e.Reconcile(ctx, source)
					last[source] = time.Now()
				}
			}
		}
	}
}

// Reopen is the operator's explicit request to restart after a clean close.
func (m *Managed) Reopen(ctx context.Context) error {
	m.mu.Lock()
	if m.owned != nil {
		if exited, _ := m.owned.Exited(); exited {
			m.owned = nil
			m.started = false
			m.Chrome.Close()
		}
	}
	m.mu.Unlock()
	return m.Ensure(ctx)
}

func (m *Managed) BeginReconcile(ctx context.Context, c Context) error {
	return m.Chrome.BeginReconcile(ctx, c)
}
func (m *Managed) EndReconcile(c Context) { m.Chrome.EndReconcile(c) }
func (m *Managed) BatchKey(i Item) string { return m.Chrome.BatchKey(i) }
func (m *Managed) ApplyBatch(ctx context.Context, c Context, items []Item) error {
	return m.Chrome.ApplyBatch(ctx, c, items)
}
