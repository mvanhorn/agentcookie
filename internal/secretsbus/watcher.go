package secretsbus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher fires a callback whenever a file under ~/.agentcookie/secrets/
// changes. Mirrors the cookies watcher pattern in internal/watcher: debounce
// rapid writes, watch parent directory (fsnotify on macOS misses sub-dir
// creation if you watch sub-dirs directly), tolerate the secrets root not
// existing yet (the friend may create it after install).
type Watcher struct {
	root     string
	debounce time.Duration
	onChange func(ctx context.Context)

	mu        sync.Mutex
	fireCount int
}

// NewWatcher constructs a watcher rooted at homeDir/.agentcookie/secrets/.
// debounce defaults to 500ms when zero.
func NewWatcher(homeDir string, debounce time.Duration, onChange func(ctx context.Context)) *Watcher {
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	return &Watcher{
		root:     SecretsRoot(homeDir),
		debounce: debounce,
		onChange: onChange,
	}
}

// Run blocks until ctx is canceled. Returns when ctx is canceled or fsnotify
// can no longer be created. Missing-root is NOT an error: the watcher polls
// for the root's appearance and starts watching it as soon as it exists.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new fsnotify watcher: %w", err)
	}
	defer fsw.Close()

	// Wait for root to exist before subscribing. fsnotify on macOS cannot
	// watch a path that doesn't exist; if the friend hasn't created the
	// secrets root yet we poll lightly until it appears.
	if !w.waitForRoot(ctx, fsw) {
		return nil
	}

	// Walk one level deep on startup so already-existing per-CLI dirs are
	// also watched. New per-CLI dirs created later trigger an event on the
	// root, at which point we add them dynamically below.
	if err := w.watchExistingChildren(fsw); err != nil {
		fmt.Fprintf(os.Stderr, "agentcookie source: secrets-bus watcher startup: %v\n", err)
	}

	debounceTimer := time.NewTimer(time.Hour)
	debounceTimer.Stop()
	defer debounceTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			// If a new per-CLI dir appeared at root, start watching it
			// too so we pick up writes inside it on the same watcher.
			if ev.Op&fsnotify.Create != 0 {
				if info, statErr := os.Stat(ev.Name); statErr == nil && info.IsDir() && filepath.Dir(ev.Name) == w.root {
					_ = fsw.Add(ev.Name)
				}
			}
			// Reset the debounce timer on any event under the root.
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer.Reset(w.debounce)

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "agentcookie source: secrets-bus watcher: fsnotify error: %v\n", err)

		case <-debounceTimer.C:
			w.mu.Lock()
			w.fireCount++
			w.mu.Unlock()
			if w.onChange != nil {
				w.onChange(ctx)
			}
		}
	}
}

// FireCount returns the number of debounced callback invocations so far.
// Test helper.
func (w *Watcher) FireCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fireCount
}

// waitForRoot blocks until the secrets root exists or ctx is canceled.
// Returns true if the root exists (and was added to the watcher), false if
// ctx canceled first.
func (w *Watcher) waitForRoot(ctx context.Context, fsw *fsnotify.Watcher) bool {
	for {
		if _, err := os.Stat(w.root); err == nil {
			if err := fsw.Add(w.root); err == nil {
				return true
			}
		}
		// Watch the parent if it exists so a Create at the parent fires
		// our re-attempt. The parent is ~/.agentcookie which is created
		// at install time, but in fresh-machine tests it may also not
		// exist yet, so fall back to a slow poll.
		parent := filepath.Dir(w.root)
		if _, perr := os.Stat(parent); perr == nil {
			_ = fsw.Add(parent)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
			// retry
		}
	}
}

// watchExistingChildren adds sub-dirs of the secrets root to the fsnotify
// subscription so writes inside per-CLI dirs fire events. New per-CLI dirs
// created later are added dynamically in Run's event loop.
func (w *Watcher) watchExistingChildren(fsw *fsnotify.Watcher) error {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_ = fsw.Add(filepath.Join(w.root, e.Name()))
	}
	return nil
}
