package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mvanhorn/agentcookie/internal/browsers"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/gecko/profiles"
	geckoreader "github.com/mvanhorn/agentcookie/internal/gecko/reader"
	"github.com/mvanhorn/agentcookie/internal/source"
	"github.com/mvanhorn/agentcookie/internal/storagecodec"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

func isGeckoSource(cfg *config.SourceConfig) bool {
	if cfg == nil || cfg.CDPSource.Enabled {
		return false
	}
	b, e := browsers.Lookup(cfg.Browser.Name)
	return e == nil && b.Family == browsers.Gecko
}

var acceptGeckoProfileChange bool

func init() {
	for _, cmd := range []*cobra.Command{sourceCmd, exportCmd, cmuxSyncCmd, agentSyncCmd} {
		cmd.Flags().BoolVar(&acceptGeckoProfileChange, "accept-profile-change", false, "accept a changed Gecko default profile identity")
	}
}

type geckoReadScopeKey struct{}

func withGeckoStoragePreview(ctx context.Context, localStorage, indexedDB bool) context.Context {
	return context.WithValue(ctx, geckoReadScopeKey{}, storagecodec.ReadSurfaces{LocalStorage: localStorage, IndexedDB: indexedDB})
}
func geckoReadScope(ctx context.Context) storagecodec.ReadSurfaces {
	scope, _ := ctx.Value(geckoReadScopeKey{}).(storagecodec.ReadSurfaces)
	return scope
}
func configuredGeckoScope(ctx context.Context, storage bool) context.Context {
	return withGeckoStoragePreview(ctx, storage && !skipLocalStorage(), storage)
}

type geckoSessionKey struct{}
type geckoExplicitProfileKey struct{}
type geckoSession struct {
	mu         sync.Mutex
	readers    map[string]*geckoreader.Reader
	identities map[string]profiles.ProfileIdentity
	accepted   map[string]bool
}

func withGeckoSession(ctx context.Context) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := &geckoSession{readers: map[string]*geckoreader.Reader{}, identities: map[string]profiles.ProfileIdentity{}, accepted: map[string]bool{}}
	return context.WithValue(ctx, geckoSessionKey{}, s), func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, r := range s.readers {
			r.Close()
		}
	}
}
func resolveGeckoProfile(cfg *config.SourceConfig) (profiles.ProfileIdentity, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return profiles.ProfileIdentity{}, e
	}
	var paths []string
	if cfg.Browser.AppPath != "" {
		paths = []string{cfg.Browser.AppPath}
	}
	resolver := profiles.Resolver{HomeDir: home, GOOS: runtime.GOOS, AppPaths: paths}
	return resolver.Resolve(cfg.Browser.Name, cfg.Browser.Profile)
}
func openGeckoReader(ctx context.Context, cfg *config.SourceConfig) (*geckoreader.Reader, error) {
	r, _, e := openGeckoReaderPinned(ctx, cfg, true)
	return r, e
}
func openGeckoReaderPinned(ctx context.Context, cfg *config.SourceConfig, allowAcceptance bool) (*geckoreader.Reader, profiles.ProfileIdentity, error) {
	identity, e := resolveGeckoProfile(cfg)
	if e != nil {
		return nil, identity, e
	}
	stateDir := filepath.Join(common.ConfigDir, "state")
	identityPath := filepath.Join(stateDir, "gecko-profile-"+cfg.Browser.Name+".json")
	var previous profiles.ProfileIdentity
	if b, e := os.ReadFile(identityPath); e == nil {
		if e = json.Unmarshal(b, &previous); e != nil {
			return nil, identity, e
		}
	} else if !os.IsNotExist(e) {
		return nil, identity, e
	}
	if e = profiles.ValidateIdentity(previous, identity, allowAcceptance && ctx.Value(geckoExplicitProfileKey{}) == true, allowAcceptance && acceptGeckoProfileChange); e != nil {
		return nil, identity, e
	}
	readScope := geckoReadScope(ctx)
	r, e := geckoreader.Open(ctx, geckoreader.Options{StorageSurfaces: &readScope, Name: cfg.Browser.Name, Profile: identity.Dir, Mode: cfg.Browser.Reader, StateDir: stateDir, BiDiPort: cfg.Browser.BiDiPort, CompanionPort: cfg.Browser.CompanionPort, StorageReadMode: cfg.Browser.StorageReadMode, Containers: cfg.Browser.Containers, ContainerSites: cfg.Browser.ContainerSites, StoragePreferences: destinationStoragePreferences(cfg)})
	if e != nil {
		return nil, identity, e
	}
	// A failed open must never commit acceptance of a changed profile.
	if e = os.MkdirAll(stateDir, 0700); e != nil {
		r.Close()
		return nil, identity, e
	}
	b, _ := json.Marshal(identity)
	temp, e := os.CreateTemp(stateDir, ".gecko-profile-*")
	if e != nil {
		r.Close()
		return nil, identity, e
	}
	defer os.Remove(temp.Name())
	if e = temp.Chmod(0600); e == nil {
		_, e = temp.Write(b)
	}
	if e == nil {
		e = temp.Sync()
	}
	closeErr := temp.Close()
	if e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(temp.Name(), identityPath)
	}
	if e != nil {
		r.Close()
		return nil, identity, e
	}
	return r, identity, nil
}
func validatePinnedGecko(cfg *config.SourceConfig, identity profiles.ProfileIdentity) error {
	current, e := resolveGeckoProfile(cfg)
	if e != nil {
		return e
	}
	return profiles.ValidateIdentity(identity, current, false, false)
}
func readGeckoSnapshot(ctx context.Context, cfg *config.SourceConfig, policy *config.Blocklist) (*source.Snapshot, error) {
	session, _ := ctx.Value(geckoSessionKey{}).(*geckoSession)
	if session == nil {
		r, identity, e := openGeckoReaderPinned(ctx, cfg, true)
		if e != nil {
			return nil, e
		}
		defer r.Close()
		if e = validatePinnedGecko(cfg, identity); e != nil {
			return nil, e
		}
		snap, e := r.Read(ctx, policy)
		if e != nil {
			return nil, e
		}
		if e = validatePinnedGecko(cfg, identity); e != nil {
			return nil, e
		}
		pinGeckoSnapshot(snap, identity)
		return snap, nil
	}
	keyBytes, _ := json.Marshal(struct {
		Browser            config.BrowserRef
		StoragePreferences [][]string
	}{cfg.Browser, destinationStoragePreferences(cfg)})
	scopeBytes, _ := json.Marshal(geckoReadScope(ctx))
	key := common.ConfigDir + "|" + string(keyBytes) + "|" + string(scopeBytes)
	scope := common.ConfigDir + "|" + cfg.Browser.Name
	session.mu.Lock()
	defer session.mu.Unlock()
	r := session.readers[key]
	if r == nil {
		// A route/configuration change must not open a second companion
		// listener against the same live profile. Retire the old reader first.
		for oldKey, oldReader := range session.readers {
			_ = oldReader.Close()
			delete(session.readers, oldKey)
		}
		var e error
		var identity profiles.ProfileIdentity
		r, identity, e = openGeckoReaderPinned(ctx, cfg, !session.accepted[scope])
		if e != nil {
			return nil, e
		}
		session.readers[key] = r
		session.identities[key] = identity
		session.accepted[scope] = true
	}
	reject := func(e error) (*source.Snapshot, error) { r.Close(); delete(session.readers, key); return nil, e }
	if e := validatePinnedGecko(cfg, session.identities[key]); e != nil {
		return reject(e)
	}
	snap, e := r.Read(ctx, policy)
	if e != nil {
		return nil, e
	}
	if e = validatePinnedGecko(cfg, session.identities[key]); e != nil {
		return reject(e)
	}
	pinGeckoSnapshot(snap, session.identities[key])
	return snap, nil
}

func pinGeckoSnapshot(snap *source.Snapshot, identity profiles.ProfileIdentity) {
	if capture, ok := snap.RoutingData.(*geckoreader.Capture); ok {
		capture.Profile = identity
	}
}

func runGeckoWatch(ctx context.Context, push func(context.Context) (int, error), verbose bool) error {
	if _, e := push(ctx); e != nil {
		return e
	}
	last := time.Now()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var debounce <-chan time.Time
	changes := func() <-chan struct{} {
		session, _ := ctx.Value(geckoSessionKey{}).(*geckoSession)
		if session == nil {
			return nil
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		for _, reader := range session.readers {
			return reader.Changes()
		}
		return nil
	}
	run := func() {
		if _, e := push(ctx); e != nil && verbose {
			fmt.Fprintf(os.Stderr, "Gecko source read failed: %v\n", e)
		}
		last = time.Now()
		timer.Stop()
		debounce = nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			run()
		case <-debounce:
			run()
		case <-changes():
			delay := max(500*time.Millisecond, time.Until(last.Add(30*time.Second)))
			timer.Reset(delay)
			debounce = timer.C
		}
	}
}

// nil retains the local/default route; explicit sink routes replace it with
// their authorized union. A nil member deliberately includes a default route.
func destinationStoragePreferences(cfg *config.SourceConfig) [][]string {
	sinks := cfg.ResolvedSinks()
	if len(sinks) == 0 {
		return nil
	}
	preferences := make([][]string, 0, len(sinks)+1)
	for _, sink := range sinks {
		if len(sink.Containers) == 0 {
			preferences = append(preferences, nil)
		} else {
			preferences = append(preferences, append([]string(nil), sink.Containers...))
		}
	}
	// Only an explicitly requested pull listener broadens named-only routes.
	if sourcePullListen != "" {
		preferences = append(preferences, nil)
	}
	return preferences
}
