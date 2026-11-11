package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

type destinationEntry struct {
	config   config.DestinationRef
	engine   *destination.Engine
	managed  *destination.Managed
	surfaces []string
}
type destinationPipeline struct {
	mu      sync.Mutex
	entries map[string]*destinationEntry
	order   []string
	nonces  map[string]time.Time
	seq     *protocol.SequenceTracker
}
type syncReply struct {
	SourceHostname string               `json:"source_hostname"`
	Status         string               `json:"status"`
	Generation     uint64               `json:"generation"`
	Sequence       int64                `json:"sequence"`
	Destinations   []destination.Status `json:"destinations"`
}
type destinationCapability struct {
	Name     string   `json:"name"`
	Surfaces []string `json:"surfaces"`
	Required bool     `json:"required"`
}
type sinkCapabilities struct {
	ProtocolVersions []int                   `json:"protocol_versions"`
	Destinations     []destinationCapability `json:"destinations"`
	Version          string                  `json:"version"`
}

// These seams let the storage-record lane reuse the exact same ownership,
// intent and recovery engine without introducing a second cookie pipeline.
var newDestinationApplier = func(m *destination.Managed) (destination.Applier, []string, error) {
	return m, []string{"cookies"}, nil
}
var convertDestinationSnapshot = destination.CookieSnapshot

func newDestinationPipeline(cfg *config.SinkConfig) (*destinationPipeline, error) {
	if !cfg.HasDestinations() && !cfg.LiveCDP.Enabled {
		return nil, nil
	}
	ds, err := cfg.ResolveDestinations()
	if err != nil {
		return nil, err
	}
	if len(ds) == 1 && ds[0].Kind == "ppcli" {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var master []byte
	if keystore.MasterKeyExists() {
		master, err = keystore.ReadMasterKey()
		if err != nil {
			return nil, fmt.Errorf("destination master key: %w", err)
		}
	}
	p := &destinationPipeline{entries: map[string]*destinationEntry{}}
	for _, d := range ds {
		if d.Kind == "ppcli" {
			p.Close()
			return nil, fmt.Errorf("ppcli must run separately from live destinations")
		}
		m := destination.NewManaged(d)
		applier, surfaces, err := newDestinationApplier(m)
		if err != nil {
			p.Close()
			return nil, err
		}
		e, err := destination.Open(filepath.Join(home, ".agentcookie", "destinations", d.Name), master, applier)
		if err != nil {
			m.Close()
			p.Close()
			return nil, err
		}
		p.entries[d.Name] = &destinationEntry{config: d, engine: e, managed: m, surfaces: surfaces}
		p.order = append(p.order, d.Name)
	}
	return p, nil
}
func (p *destinationPipeline) Close() {
	if p == nil {
		return
	}
	for _, d := range p.entries {
		if d.managed != nil {
			d.managed.Close()
		}
	}
}
func (p *destinationPipeline) Start(ctx context.Context) {
	if p == nil {
		return
	}
	for _, name := range p.order {
		d := p.entries[name]
		if d.config.Kind == "live_chrome" && d.config.Chrome.Start == "on_boot" {
			startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := d.managed.Ensure(startCtx); err != nil {
				fmt.Fprintf(os.Stderr, "agentcookie destination %s: %v\n", name, err)
			}
			for _, source := range d.engine.Sources() {
				_, _ = d.engine.Reconcile(startCtx, source)
			}
			cancel()
		}
		go func() { _ = d.managed.Run(ctx, d.engine) }()
	}
}
func (p *destinationPipeline) names(destinationName string) ([]string, error) {
	if destinationName != "" {
		if _, ok := p.entries[destinationName]; !ok {
			return nil, fmt.Errorf("unknown destination %q", destinationName)
		}
		return []string{destinationName}, nil
	}
	return append([]string(nil), p.order...), nil
}
func (p *destinationPipeline) capabilities() sinkCapabilities {
	c := sinkCapabilities{ProtocolVersions: []int{1, 2, 3}, Version: Version}
	for _, name := range p.order {
		d := p.entries[name]
		c.Destinations = append(c.Destinations, destinationCapability{Name: name, Surfaces: d.surfaces, Required: d.config.Required})
	}
	return c
}
func (p *destinationPipeline) status(source string) syncReply {
	r := syncReply{Status: "ok", SourceHostname: source}
	if p.seq != nil {
		r.Sequence = p.seq.Last(source)
	}
	for _, name := range p.order {
		if st, ok := p.entries[name].engine.Status(source); ok {
			st.Name = name
			st.Required = p.entries[name].config.Required
			r.Destinations = append(r.Destinations, st)
			if st.Accepted > r.Generation {
				r.Generation = st.Accepted
			}
			if !st.Ready {
				r.Status = "degraded"
			}
		}
	}
	return r
}
func (p *destinationPipeline) apply(ctx context.Context, env protocol.SyncEnvelope, matcher *protocol.BlocklistMatcher) (syncReply, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := syncReply{Status: "ok", SourceHostname: env.SourceHostname, Sequence: env.Sequence}
	names, err := p.names(env.Destination)
	if err != nil {
		return r, err
	}
	for _, name := range names {
		entry := p.entries[name]
		snap, excluded, err := convertDestinationSnapshot(env)
		if err != nil {
			return r, err
		}
		if env.Snapshot == nil {
			previous, _ := entry.engine.Status(env.SourceHostname)
			snap.Generation = previous.Accepted + 1
		}
		// Apply current sink policy to prior desired items too: a newly excluded
		// account must revoke owned records even when this read is incomplete.
		entry.engine.FilterSnapshot(&snap, matcher.ShouldSyncHost)
		supported := map[string]bool{}
		for _, surface := range entry.surfaces {
			supported[surface] = true
		}
		for surface := range snap.Surfaces {
			if !supported[surface] {
				snap.Surfaces[surface] = "unsupported"
			}
		}
		filtered := snap.Items[:0]
		for _, item := range snap.Items {
			if supported[item.Surface] {
				filtered = append(filtered, item)
			}
		}
		snap.Items = filtered
		if prior, ok := entry.engine.Status(env.SourceHostname); ok && snap.Generation <= prior.Accepted {
			return r, fmt.Errorf("stale generation %d (accepted %d)", snap.Generation, prior.Accepted)
		}
		snap.CapExcluded = excluded
		st, applyErr := entry.engine.Accept(ctx, env.SourceHostname, snap)
		st.Name = name
		st.Required = p.entries[name].config.Required
		st.CapExcluded = excluded
		r.Generation = snap.Generation
		r.Destinations = append(r.Destinations, st)
		if applyErr != nil {
			if st.Accepted < snap.Generation {
				return r, applyErr
			}
			st.Failed = append(st.Failed, "browser application pending")
			st.Ready = false
			r.Destinations[len(r.Destinations)-1] = st
		}
		if !st.Ready {
			r.Status = "degraded"
		}
	}
	return r, nil
}
func (p *destinationPipeline) register(mux *http.ServeMux, secret string) {
	if p == nil {
		return
	}
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return false
		}
		if transport.VerifyRequest(r, secret, time.Now(), 0) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		return true
	}
	mux.HandleFunc("/destination", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if transport.VerifyRequest(r, secret, time.Now(), 0) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		if p.nonces == nil {
			p.nonces = map[string]time.Time{}
		}
		now := time.Now()
		for nonce, at := range p.nonces {
			if now.Sub(at) > 5*time.Minute {
				delete(p.nonces, nonce)
			}
		}
		nonce := r.Header.Get(transport.HeaderNonce)
		_, replay := p.nonces[nonce]
		p.nonces[nonce] = now
		p.mu.Unlock()
		if replay {
			http.Error(w, "replayed command", http.StatusConflict)
			return
		}
		sealed, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if readErr != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		plain, openErr := transport.OpenWithSecret(sealed, secret)
		if openErr != nil {
			http.Error(w, "unauthorized action", http.StatusUnauthorized)
			return
		}
		var action destinationAction
		if err := json.Unmarshal(plain, &action); err != nil {
			http.Error(w, "invalid action", 400)
			return
		}
		if action.Nonce != nonce {
			http.Error(w, "action nonce authentication failed", http.StatusUnauthorized)
			return
		}
		d, ok := p.entries[action.Name]
		if !ok {
			http.Error(w, "unknown destination", 400)
			return
		}
		if action.Action != "open" && action.Action != "repair" {
			http.Error(w, "unknown action", 400)
			return
		}
		if action.Action == "repair" && !action.Confirmed {
			http.Error(w, "repair requires explicit confirmation", 400)
			return
		}
		sources := d.engine.Sources()
		if len(sources) == 0 {
			http.Error(w, "no desired state accepted yet", http.StatusConflict)
			return
		}
		if action.Action == "open" {
			if err := d.managed.Reopen(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		for _, source := range sources {
			var st destination.Status
			var err error
			if action.Action == "repair" {
				st, err = d.engine.Repair(r.Context(), source, true)
			} else {
				st, err = d.engine.Reconcile(r.Context(), source)
			}
			if err != nil || !st.Ready {
				http.Error(w, "destination not ready; inspect /status before opening", http.StatusConflict)
				return
			}
		}
		if action.Action == "open" {
			if err := d.managed.OpenWindow(r.Context()); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": action.Action, "destination": action.Name})
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			writeAuthenticatedJSON(w, r, secret, p.capabilities())
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		source := r.URL.Query().Get("source")
		if source == "" {
			http.Error(w, "source is required", 400)
			return
		}
		writeAuthenticatedJSON(w, r, secret, p.status(source))
	})
}
func (p *destinationPipeline) allStatus() []destination.Status {
	var out []destination.Status
	if p == nil {
		return out
	}
	for _, name := range p.order {
		for _, source := range p.entries[name].engine.Sources() {
			st, _ := p.entries[name].engine.Status(source)
			st.Name = name
			st.Required = p.entries[name].config.Required
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *destinationPipeline) validate(env protocol.SyncEnvelope) error {
	if _, err := p.names(env.Destination); err != nil {
		return err
	}
	if env.ProtocolVersion >= 3 && env.Snapshot == nil {
		return fmt.Errorf("v3 snapshot metadata required")
	}
	snap, _, err := convertDestinationSnapshot(env)
	if err != nil {
		return err
	}
	return destination.ValidateSnapshot(snap)
}

func writeAuthenticatedJSON(w http.ResponseWriter, r *http.Request, secret string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode response", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(transport.HeaderResponseMAC, transport.ResponseMAC(secret, r.Header.Get(transport.HeaderNonce), data))
	_, _ = w.Write(data)
}

func startDestinationStatusUpdates(ctx context.Context, rt *sinkRuntime) {
	if rt.destinations == nil {
		return
	}
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		previous := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				statuses := rt.destinations.allStatus()
				encoded, _ := json.Marshal(statuses)
				if string(encoded) == previous {
					continue
				}
				previous = string(encoded)
				rt.mu.Lock()
				rt.st.Destinations = statuses
				if rt.writer != nil {
					_ = rt.writer.Save(rt.st)
				}
				rt.mu.Unlock()
			}
		}
	}()
}
