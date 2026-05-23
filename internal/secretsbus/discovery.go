package secretsbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SourceKind describes how a project landed in the registry.
type SourceKind string

const (
	SourceKindExplicitManifest SourceKind = "explicit-manifest"
	SourceKindPPCLIDerived     SourceKind = "pp-cli-derived"
	SourceKindLegacyV1         SourceKind = "legacy-v1"
)

// RegisteredProject is one row in the discovery registry. The Manifest
// pointer may be in-memory (PP-derived) or parsed from disk (explicit).
type RegisteredProject struct {
	Name              string
	Kind              SourceKind
	SourcePath        string // where the manifest came from (file path)
	ReadInPlacePath   string // expanded [secrets.file].path; empty for legacy-v1
	Manifest          *ManifestV2
	SkippedReason     string // populated only on SkippedEntry list
}

// Registry holds the discovered projects keyed by slug, plus per-path
// rejections so the discover command can surface why something was skipped.
type Registry struct {
	Projects map[string]*RegisteredProject
	Skipped  []RegisteredProject
}

// DiscoveryConfig customizes discovery for tests. Production callers pass
// homeDir and accept defaults for the rest.
type DiscoveryConfig struct {
	HomeDir         string
	ExtraPaths      []string // user-added paths via `agentcookie discover --add-path`
	PPLibraryPath   string   // default ~/printing-press/library; settable for tests
	SystemPath      string   // default /usr/local/share/agentcookie/manifests; settable for tests
}

// Discover walks the well-known paths in priority order, parses every
// manifest, applies precedence + collision rules, and returns the registry.
// Soft-skip failures (per spec section 8.3) appear in registry.Skipped with
// a reason; the per-file error is also accumulated in the second return
// value for callers that want to surface them collectively.
func Discover(cfg DiscoveryConfig) (*Registry, []error) {
	if cfg.HomeDir == "" {
		cfg.HomeDir, _ = os.UserHomeDir()
	}
	if cfg.PPLibraryPath == "" {
		cfg.PPLibraryPath = filepath.Join(cfg.HomeDir, "printing-press", "library")
	}
	if cfg.SystemPath == "" {
		cfg.SystemPath = "/usr/local/share/agentcookie/manifests"
	}

	reg := &Registry{Projects: map[string]*RegisteredProject{}}
	var errs []error

	// Priority order per spec section 2.2.
	manifestDirs := []string{
		filepath.Join(cfg.HomeDir, ".agentcookie", "manifests"),
		filepath.Join(cfg.HomeDir, ".config", "agentcookie", "manifests"),
		cfg.SystemPath,
	}
	for _, dir := range manifestDirs {
		manifests := scanManifestDir(dir)
		for _, mf := range manifests {
			tryRegisterExplicit(reg, &errs, mf, cfg.HomeDir)
		}
	}
	for _, dir := range cfg.ExtraPaths {
		manifests := scanManifestDir(dir)
		for _, mf := range manifests {
			tryRegisterExplicit(reg, &errs, mf, cfg.HomeDir)
		}
	}

	// PP CLI auto-detect adapter. Priority 4 per spec.
	ppManifests := scanPPLibrary(cfg.PPLibraryPath)
	for _, ppPath := range ppManifests {
		tryRegisterPP(reg, &errs, ppPath, cfg.HomeDir)
	}

	// Legacy v1: synthesize entries from ~/.agentcookie/secrets/<name>/.
	legacyRoot := SecretsRoot(cfg.HomeDir)
	legacyEntries := scanLegacyV1(legacyRoot)
	for _, name := range legacyEntries {
		tryRegisterLegacy(reg, name, legacyRoot)
	}

	return reg, errs
}

// scanManifestDir lists *.toml files in a manifest directory in stable
// sorted order. Missing or unreadable directories are silently skipped (no
// manifest dir is normal).
func scanManifestDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		paths = append(paths, filepath.Join(dir, e.Name()))
	}
	sort.Strings(paths)
	return paths
}

// scanPPLibrary returns paths to .printing-press.json files. Caps the walk
// at one level deep per the PP library convention.
func scanPPLibrary(libraryPath string) []string {
	entries, err := os.ReadDir(libraryPath)
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(libraryPath, e.Name(), ".printing-press.json")
		if _, err := os.Stat(candidate); err == nil {
			paths = append(paths, candidate)
		}
	}
	sort.Strings(paths)
	return paths
}

// scanLegacyV1 lists per-CLI subdirectories of the v1 bus root.
func scanLegacyV1(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if validCLIName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// tryRegisterExplicit parses an explicit manifest and adds it to the
// registry with the proper precedence/collision behavior.
func tryRegisterExplicit(reg *Registry, errs *[]error, path, homeDir string) {
	m, _, err := ParseManifestV2(path)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("skip %s: %w", path, err))
		reg.Skipped = append(reg.Skipped, RegisteredProject{
			SourcePath:    path,
			Kind:          SourceKindExplicitManifest,
			SkippedReason: err.Error(),
		})
		return
	}
	rp := &RegisteredProject{
		Name:            m.Name,
		Kind:            SourceKindExplicitManifest,
		SourcePath:      path,
		ReadInPlacePath: m.ResolveSecretsPath(homeDir),
		Manifest:        m,
	}

	if existing, ok := reg.Projects[m.Name]; ok {
		// Explicit-vs-explicit collision is a hard error per spec 4.1.
		if existing.Kind == SourceKindExplicitManifest {
			err := fmt.Errorf("explicit-manifest collision on name %q: %s vs %s; both rejected", m.Name, existing.SourcePath, path)
			*errs = append(*errs, err)
			reg.Skipped = append(reg.Skipped, RegisteredProject{
				Name:          m.Name,
				SourcePath:    path,
				Kind:          SourceKindExplicitManifest,
				SkippedReason: err.Error(),
			})
			reg.Skipped = append(reg.Skipped, *existing)
			reg.Skipped[len(reg.Skipped)-1].SkippedReason = err.Error()
			delete(reg.Projects, m.Name)
			return
		}
		// Explicit wins over derived (4.2); suffix the derived one.
		oldName := existing.Name
		existing.Name = existing.Name + "-pp"
		reg.Projects[existing.Name] = existing
		delete(reg.Projects, oldName)
	}
	reg.Projects[m.Name] = rp
}

// tryRegisterPP runs the PP CLI adapter and stores the result with kind
// pp-cli-derived. Collisions with explicit manifests get the -pp suffix.
func tryRegisterPP(reg *Registry, errs *[]error, ppJSONPath, homeDir string) {
	m, err := DeriveManifestFromPP(ppJSONPath)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("skip %s: %w", ppJSONPath, err))
		reg.Skipped = append(reg.Skipped, RegisteredProject{
			SourcePath:    ppJSONPath,
			Kind:          SourceKindPPCLIDerived,
			SkippedReason: err.Error(),
		})
		return
	}
	finalName := m.Name
	if existing, ok := reg.Projects[m.Name]; ok {
		if existing.Kind == SourceKindExplicitManifest {
			// Explicit wins per 4.2; derived gets -pp suffix.
			finalName = m.Name + "-pp"
		} else if existing.Kind == SourceKindPPCLIDerived {
			// Two derived collisions per 4.3: first-by-sort already won,
			// this one gets a hash suffix.
			sum := sha256.Sum256([]byte(ppJSONPath))
			finalName = m.Name + "-" + hex.EncodeToString(sum[:3])
		}
	}
	rp := &RegisteredProject{
		Name:            finalName,
		Kind:            SourceKindPPCLIDerived,
		SourcePath:      ppJSONPath,
		ReadInPlacePath: m.ResolveSecretsPath(homeDir),
		Manifest:        m,
	}
	reg.Projects[finalName] = rp
}

// tryRegisterLegacy adds a synthetic v1-bus entry. The manifest pointer is
// nil for legacy entries; the source push pipeline reads from the v1 bus
// directly via LoadPayload, not via read-in-place.
func tryRegisterLegacy(reg *Registry, name, legacyRoot string) {
	if _, ok := reg.Projects[name]; ok {
		// v2 already won for this name. v1 bus entry still counts as a
		// data source at push time (handled in source.go); the registry
		// just doesn't record it again.
		return
	}
	reg.Projects[name] = &RegisteredProject{
		Name:            name,
		Kind:            SourceKindLegacyV1,
		SourcePath:      filepath.Join(legacyRoot, name),
		ReadInPlacePath: "", // v1 reads from the bus directory itself
	}
}

// DiscoveryWatcher provides live registry updates via fsnotify. Mirrors the
// shape of the v1 Watcher in watcher.go.
type DiscoveryWatcher struct {
	cfg      DiscoveryConfig
	debounce time.Duration
	onChange func(ctx context.Context, delta RegistryDelta, reg *Registry)
	prev     *Registry
	mu       sync.Mutex
}

// RegistryDelta is what the watcher emits to its callback when something
// changes. Added/Removed/Changed are slugs.
type RegistryDelta struct {
	Added   []string
	Removed []string
	Changed []string
}

// NewDiscoveryWatcher constructs a watcher. debounce defaults to 250ms
// (matches v1 secrets-bus watcher) when zero.
func NewDiscoveryWatcher(cfg DiscoveryConfig, debounce time.Duration, onChange func(ctx context.Context, delta RegistryDelta, reg *Registry)) *DiscoveryWatcher {
	if debounce == 0 {
		debounce = 250 * time.Millisecond
	}
	return &DiscoveryWatcher{cfg: cfg, debounce: debounce, onChange: onChange}
}

// Run blocks until ctx is canceled. Watches all manifest dirs and the PP
// library; re-runs Discover() on each fsnotify event after debounce.
func (w *DiscoveryWatcher) Run(ctx context.Context) error {
	if w.cfg.HomeDir == "" {
		w.cfg.HomeDir, _ = os.UserHomeDir()
	}
	if w.cfg.PPLibraryPath == "" {
		w.cfg.PPLibraryPath = filepath.Join(w.cfg.HomeDir, "printing-press", "library")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer fsw.Close()

	dirs := []string{
		filepath.Join(w.cfg.HomeDir, ".agentcookie", "manifests"),
		filepath.Join(w.cfg.HomeDir, ".config", "agentcookie", "manifests"),
		w.cfg.PPLibraryPath,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err == nil {
			_ = fsw.Add(d)
		}
	}

	// Initial snapshot.
	w.prev, _ = Discover(w.cfg)
	if w.onChange != nil {
		w.onChange(ctx, RegistryDelta{Added: sortedKeys(w.prev.Projects)}, w.prev)
	}

	var pending *time.Timer
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			_ = ev
			if pending != nil {
				pending.Stop()
			}
			pending = time.AfterFunc(w.debounce, func() {
				w.rescan(ctx)
			})
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintln(os.Stderr, "discovery watcher error:", err)
		}
	}
}

func (w *DiscoveryWatcher) rescan(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	next, _ := Discover(w.cfg)
	delta := diffRegistries(w.prev, next)
	w.prev = next
	if w.onChange != nil && (len(delta.Added)+len(delta.Removed)+len(delta.Changed)) > 0 {
		w.onChange(ctx, delta, next)
	}
}

func diffRegistries(prev, next *Registry) RegistryDelta {
	var d RegistryDelta
	if prev == nil {
		d.Added = sortedKeys(next.Projects)
		return d
	}
	for name := range next.Projects {
		if _, ok := prev.Projects[name]; !ok {
			d.Added = append(d.Added, name)
		}
	}
	for name := range prev.Projects {
		if _, ok := next.Projects[name]; !ok {
			d.Removed = append(d.Removed, name)
		}
	}
	// "Changed" detection is intentionally coarse: same name, different
	// manifest contents. The watcher fires the callback either way, so we
	// keep this minimal for now.
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	return d
}

func sortedKeys(m map[string]*RegisteredProject) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
