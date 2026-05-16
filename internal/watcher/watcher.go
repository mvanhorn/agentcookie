// Package watcher runs the source-side fsnotify loop. It watches the parent
// directory of Chrome's Cookies SQLite (watching the file directly misses
// Chrome's rename-on-write pattern), debounces rapid writes, rate-caps push
// frequency, and tolerates push failures with exponential backoff.
package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config controls the watcher loop.
type Config struct {
	// CookiesPath is the absolute path to Chrome's Cookies SQLite. The watcher
	// watches the file's parent directory.
	CookiesPath string

	// Push is the per-event callback the watcher invokes after debounce. It
	// returns the number of cookies pushed and an error (recorded but does
	// not stop the watcher).
	Push func(ctx context.Context) (int, error)

	// OnEvent fires for every observed fs event, useful for verbose logging.
	// Optional.
	OnEvent func(Event)

	// Debounce: how long to wait after the last event before triggering a push.
	// Defaults to 500ms.
	Debounce time.Duration

	// MinInterval: minimum gap between successive pushes regardless of event
	// rate. Defaults to 2s.
	MinInterval time.Duration

	// BaselineTick: even with no fs events, run a push every BaselineTick.
	// Defends against fsnotify event loss on macOS. Defaults to 30s.
	BaselineTick time.Duration

	// MaxBackoff: cap on exponential backoff after a push failure. Defaults to
	// 60s.
	MaxBackoff time.Duration
}

func (c Config) debounce() time.Duration {
	if c.Debounce > 0 {
		return c.Debounce
	}
	return 500 * time.Millisecond
}

func (c Config) minInterval() time.Duration {
	if c.MinInterval > 0 {
		return c.MinInterval
	}
	return 2 * time.Second
}

func (c Config) baselineTick() time.Duration {
	if c.BaselineTick > 0 {
		return c.BaselineTick
	}
	return 30 * time.Second
}

func (c Config) maxBackoff() time.Duration {
	if c.MaxBackoff > 0 {
		return c.MaxBackoff
	}
	return 60 * time.Second
}

// Watcher runs the watch loop. Construct via New, then call Run.
type Watcher struct {
	cfg Config

	mu         sync.Mutex
	lastPush   time.Time
	lastErr    error
	pushCount  int
	errorCount int
}

// New validates Config and returns a Watcher.
func New(cfg Config) (*Watcher, error) {
	if cfg.CookiesPath == "" {
		return nil, fmt.Errorf("CookiesPath is required")
	}
	if cfg.Push == nil {
		return nil, fmt.Errorf("Push callback is required")
	}
	if _, err := os.Stat(filepath.Dir(cfg.CookiesPath)); err != nil {
		return nil, fmt.Errorf("watch parent dir: %w", err)
	}
	return &Watcher{cfg: cfg}, nil
}

// Run blocks. Returns when ctx is canceled.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new fsnotify watcher: %w", err)
	}
	defer fsw.Close()

	parent := filepath.Dir(w.cfg.CookiesPath)
	if err := fsw.Add(parent); err != nil {
		return fmt.Errorf("watch %s: %w", parent, err)
	}

	// Kick off one push at startup so the sink is current from t=0.
	go w.runOne(ctx, "startup")

	debounceTimer := time.NewTimer(time.Hour)
	debounceTimer.Stop()
	defer debounceTimer.Stop()

	baselineTicker := time.NewTicker(w.cfg.baselineTick())
	defer baselineTicker.Stop()

	pendingEvent := false

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// Filter to events on the Cookies file (or its WAL/journal companions).
			if !w.isInteresting(ev.Name) {
				continue
			}
			if w.cfg.OnEvent != nil {
				w.cfg.OnEvent(Event{Source: "fsnotify", Op: ev.Op.String(), Path: ev.Name, At: time.Now()})
			}
			// Reset the debounce timer.
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer.Reset(w.cfg.debounce())
			pendingEvent = true

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "agentcookie source --watch: fsnotify error: %v\n", err)

		case <-debounceTimer.C:
			pendingEvent = false
			if !w.respectRateCap() {
				continue
			}
			go w.runOne(ctx, "fs-event")

		case <-baselineTicker.C:
			if pendingEvent {
				continue
			}
			if !w.respectRateCap() {
				continue
			}
			go w.runOne(ctx, "baseline-tick")
		}
	}
}

// isInteresting returns true if the event path is the Cookies file or one of
// Chrome's WAL / journal companions.
func (w *Watcher) isInteresting(p string) bool {
	base := filepath.Base(p)
	cookies := filepath.Base(w.cfg.CookiesPath)
	switch base {
	case cookies, cookies + "-wal", cookies + "-journal", cookies + "-shm":
		return true
	}
	return false
}

// respectRateCap returns true when enough time has passed since the last push
// to begin another one.
func (w *Watcher) respectRateCap() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.lastPush.IsZero() && time.Since(w.lastPush) < w.cfg.minInterval() {
		return false
	}
	w.lastPush = time.Now()
	return true
}

func (w *Watcher) runOne(ctx context.Context, reason string) {
	pushCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	n, err := w.cfg.Push(pushCtx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.lastErr = fmt.Errorf("%s: %w", reason, err)
		w.errorCount++
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: push (%s) failed: %v\n", reason, err)
		return
	}
	w.pushCount++
	if n > 0 {
		fmt.Fprintf(os.Stderr, "agentcookie source --watch: pushed %d cookies (%s)\n", n, reason)
	}
}

// Stats returns the current push and error counts plus the last seen error.
func (w *Watcher) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Stats{
		PushCount:  w.pushCount,
		ErrorCount: w.errorCount,
		LastPush:   w.lastPush,
		LastError:  w.lastErr,
	}
}

// Stats is the watcher's observable state.
type Stats struct {
	PushCount  int
	ErrorCount int
	LastPush   time.Time
	LastError  error
}

// Event is the structured form passed to Config.OnEvent. Useful for logs.
type Event struct {
	Source string
	Op     string
	Path   string
	At     time.Time
}

// String renders an Event in a one-line log-friendly form.
func (e Event) String() string {
	return fmt.Sprintf("%s %s %s", e.Source, e.Op, e.Path)
}
