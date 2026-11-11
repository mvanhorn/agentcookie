// Package destination reconciles durable, source-owned records into live
// browser contexts. Browser values are never accepted as proof of ownership.
package destination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type Item struct {
	Surface    string          `json:"surface"`
	Scope      string          `json:"scope"`
	Key        string          `json:"key"`
	Value      json.RawMessage `json:"value,omitempty"`
	Hash       string          `json:"value_hash"`
	Generation uint64          `json:"generation"`
	Deleted    bool            `json:"deleted,omitempty"`
}

func digest(v []byte) string { h := sha256.Sum256(v); return hex.EncodeToString(h[:]) }
func NewItem(surface, scope, key string, value []byte) Item {
	return Item{Surface: surface, Scope: scope, Key: key, Value: append(json.RawMessage(nil), value...), Hash: Hash(value)}
}

// Hash canonicalizes JSON object keys without losing numeric precision. Codec
// payloads must already preserve structured-clone order in their tagged arrays.
func Hash(value []byte) string {
	var v any
	d := json.NewDecoder(strings.NewReader(string(value)))
	d.UseNumber()
	if d.Decode(&v) == nil {
		if b, e := json.Marshal(v); e == nil {
			return digest(b)
		}
	}
	return digest(value)
}
func (i Item) ID() string {
	b, _ := json.Marshal([]string{i.Surface, i.Scope, i.Key})
	return digest(b)
}

type Context struct {
	OwnedProfile string `json:"owned_profile,omitempty"` // Set only by a verified owned Chrome, never attach/private contexts.
	ID           string `json:"id"`
	Incarnation  string `json:"incarnation"`
	Default      bool   `json:"default"`
}

func (c Context) Key() string { return digest([]byte(c.ID + "\x00" + c.Incarnation)) }

type Applier interface {
	Contexts(context.Context) ([]Context, error)
	Read(context.Context, Context, Item) (string, bool, error)
	Set(context.Context, Context, Item) error
	Delete(context.Context, Context, Item) error
}

// BatchApplier is used by IndexedDB: all intents are durable before the
// transaction, and each result is value-verified after the transaction commits.
// ReconcileApplier snapshots browser values once per reconciliation, and
// invalidates that snapshot after writes before verifying the result.
type ReconcileApplier interface {
	BeginReconcile(context.Context, Context) error
	EndReconcile(Context)
}

// RecoveryPreparer batches read-only verification of superseded unfinished
// intents. It must not depend on the current desired schema or mutate storage.
type RecoveryPreparer interface {
	PrepareRecovery(context.Context, Context, []Item) error
}
type BatchApplier interface {
	BatchKey(Item) string
	ApplyBatch(context.Context, Context, []Item) error
}
type Snapshot struct {
	CapExcluded        int               `json:"cap_excluded,omitempty"`
	ReceivedAt         time.Time         `json:"received_at,omitempty"` // Sink-assigned acceptance time, never trusted from the sender.
	Generation         uint64            `json:"generation"`
	Items              []Item            `json:"items"`
	Surfaces           map[string]string `json:"surfaces"`
	Scopes             map[string]string `json:"scopes,omitempty"` // surface + NUL + scope
	Sites              map[string]string `json:"sites,omitempty"`
	AuthoritativeEmpty bool              `json:"authoritative_empty,omitempty"`
}
type RepairNeeded struct {
	Source     string `json:"source"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason"`
}
type Status struct {
	PendingSince   time.Time         `json:"pending_since,omitempty"`
	LastAcceptedAt time.Time         `json:"last_accepted_at,omitempty"`
	LastAppliedAt  time.Time         `json:"last_applied_at,omitempty"`
	Running        bool              `json:"running"`
	Required       bool              `json:"required"`
	Name           string            `json:"name"`
	Accepted       uint64            `json:"accepted"`
	Applied        map[string]uint64 `json:"applied"`
	Pending        map[string]int    `json:"pending"`
	Failed         []string          `json:"failed"`
	Conflicts      int               `json:"conflicts"`
	CapExcluded    int               `json:"cap_excluded"`
	Ready          bool              `json:"ready"`
	RepairNeeded   *RepairNeeded     `json:"repair_needed,omitempty"`
}
type desired struct {
	PendingSince  time.Time          `json:"pending_since,omitempty"`
	LastAppliedAt time.Time          `json:"last_applied_at,omitempty"`
	OwnedDefaults map[string]string  `json:"owned_defaults,omitempty"` // Verified profile -> latest default-context ledger key.
	Contexts      map[string]Context `json:"contexts,omitempty"`
	Generation    uint64             `json:"generation"`
	Items         map[string]Item    `json:"items"`
	Surfaces      map[string]string  `json:"surfaces"`
	Conflicts     int                `json:"conflicts"`
	Repair        *RepairNeeded      `json:"repair_needed,omitempty"`
}
type ledger struct {
	Groups  map[string]uint64 `json:"group_applied,omitempty"`
	Context Context           `json:"context"`
	Items   map[string]Item   `json:"items"`
	Applied map[string]uint64 `json:"applied"`
}
type intent struct {
	Abandoned bool      `json:"abandoned,omitempty"` // Superseded attempts are not completed writes.
	ID        string    `json:"id"`
	Context   Context   `json:"context"`
	Item      Item      `json:"item"`
	Done      bool      `json:"done"`
	StartedAt time.Time `json:"started_at"`
}
type sourceState struct {
	JournalError error // An uncertain append forbids further writes until reload.
	LogEntries   int
	Active       []Context
	Failures     []string
	Accepted     Snapshot
	Desired      desired
	Ledgers      map[string]*ledger
	Intents      map[string]intent
}
type Engine struct {
	mu      sync.Mutex
	dir     string
	key     []byte
	applier Applier
	sources map[string]*sourceState
	Now     func() time.Time // Optional deterministic clock for tests.
	Fault   func(stage string) error
}

func validName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
func Open(dir string, masterKey []byte, applier Applier) (*Engine, error) {
	if applier == nil {
		return nil, errors.New("nil destination applier")
	}
	if len(masterKey) != 0 && len(masterKey) != 32 {
		return nil, errors.New("master key must be 32 bytes")
	}
	e := &Engine{dir: dir, key: append([]byte(nil), masterKey...), applier: applier, sources: map[string]*sourceState{}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !validName(entry.Name()) {
			return nil, errors.New("invalid source directory")
		}
		s, err := e.load(entry.Name())
		if err != nil {
			return nil, err
		}
		e.sources[entry.Name()] = s
	}
	return e, nil
}
func blankState() *sourceState {
	return &sourceState{Desired: desired{Items: map[string]Item{}, Surfaces: map[string]string{}}, Ledgers: map[string]*ledger{}, Intents: map[string]intent{}}
}
func (e *Engine) load(source string) (*sourceState, error) {
	s := blankState()
	base := filepath.Join(e.dir, source)
	if err := e.read(filepath.Join(base, "desired.json"), &s.Desired); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("desired %s: %w", source, err)
	}
	if err := e.read(filepath.Join(base, "accepted.json"), &s.Accepted); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("accepted %s: %w", source, err)
	}
	entries, _ := os.ReadDir(filepath.Join(base, "applied"))
	badLedger := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		var l ledger
		if err := e.read(filepath.Join(base, "applied", entry.Name()), &l); err != nil || l.Items == nil {
			badLedger = true
			continue
		}
		s.Ledgers[l.Context.Key()] = &l
	}
	logs, logErr := e.readLog(filepath.Join(base, "intents.log"))
	if logErr != nil && !os.IsNotExist(logErr) {
		// Never append behind an unreadable tail: all subsequent durable
		// intents would disappear on the next reload. Preserve the readable
		// prefix atomically and retain an explicit repair warning for any
		// ownership that may have been lost beyond the corruption.
		s.Desired.Repair = &RepairNeeded{Source: source, Generation: s.Desired.Generation, Reason: "intent journal corrupt; readable prefix retained, ownership beyond corruption cannot be proven"}
		if err := e.saveDesired(source, s); err != nil {
			return nil, err
		}
		if err := e.replaceLog(filepath.Join(base, "intents.log"), logs); err != nil {
			return nil, err
		}
	}
	s.LogEntries = len(logs)
	for _, in := range logs {
		s.Intents[in.ID] = in
	}
	missingLedger := len(s.Ledgers) == 0 || badLedger
	for key := range s.Desired.Contexts {
		if _, ok := s.Ledgers[key]; !ok {
			missingLedger = true
		}
	}
	if s.Desired.Generation > 0 && missingLedger && (logErr != nil || len(logs) == 0) {
		s.Desired.Repair = &RepairNeeded{Source: source, Generation: s.Desired.Generation, Reason: "ledger and intent history unavailable; items revoked before the loss cannot be identified and will not be deleted"}
	}
	// Completed writes and unfinished deletion intents are durable ownership
	// evidence: deletion is journaled only after ownership was established.
	// Rebuild from the ordered log, relinquishing only completed deletions,
	// never inferring ownership from coincident browser keys.
	for _, in := range logs {
		if in.Abandoned || !in.Done && !in.Item.Deleted {
			continue
		}
		l := s.Ledgers[in.Context.Key()]
		if l == nil {
			l = &ledger{Context: in.Context, Items: map[string]Item{}, Applied: map[string]uint64{}}
			s.Ledgers[in.Context.Key()] = l
		}
		old, ok := l.Items[in.Item.ID()]
		if ok && old.Generation > in.Item.Generation {
			continue
		}
		if in.Item.Deleted && in.Done {
			delete(l.Items, in.Item.ID())
		} else {
			l.Items[in.Item.ID()] = in.Item
		}
	}
	if s.Desired.Items == nil {
		s.Desired.Items = map[string]Item{}
	}
	if s.Desired.Surfaces == nil {
		s.Desired.Surfaces = map[string]string{}
	}
	return s, nil
}
func (e *Engine) saveDesired(source string, s *sourceState) error {
	return e.write(filepath.Join(e.dir, source, "desired.json"), s.Desired)
}
func (e *Engine) saveLedger(source string, l *ledger) error {
	if err := e.fault("before-ledger-save"); err != nil {
		return err
	}
	return e.write(filepath.Join(e.dir, source, "applied", l.Context.Key()+".json"), l)
}
func (e *Engine) fault(stage string) error {
	if e.Fault != nil {
		return e.Fault(stage)
	}
	return nil
}
func (e *Engine) ownedByOther(source, id string) bool {
	for name, s := range e.sources {
		if name == source {
			continue
		}
		if i, ok := s.Desired.Items[id]; ok && !i.Deleted {
			return true
		}
		for _, l := range s.Ledgers {
			if !currentOwnershipContext(s, l.Context) {
				continue
			}
			if _, ok := l.Items[id]; ok {
				return true
			}
		}
		for _, in := range s.Intents {
			if !in.Done && in.Item.ID() == id && currentOwnershipContext(s, in.Context) {
				return true
			}
		}
	}
	return false
}

// Ownership survives process restart only for a verified dedicated default
// profile. An arbitrary attach endpoint or recycled private context never gets it.
func sameOwnedDefault(a, b Context) bool {
	return a.Default && b.Default && a.OwnedProfile != "" && a.OwnedProfile == b.OwnedProfile
}
func currentOwnershipContext(s *sourceState, c Context) bool {
	if c.Default && c.OwnedProfile != "" {
		return s.Desired.OwnedDefaults[c.OwnedProfile] == c.Key()
	}
	if len(s.Active) == 0 {
		return true
	}
	for _, active := range s.Active {
		if active.Key() == c.Key() {
			return true
		}
	}
	return false
}
func scopeState(s Snapshot, i Item) string {
	if v, ok := s.Scopes[i.Surface+"\x00"+i.Scope]; ok {
		return v
	}
	return s.Surfaces[i.Surface]
}
func siteState(s Snapshot, i Item) string {
	if v, ok := s.Sites[i.Scope]; ok {
		return v
	}
	host := strings.TrimPrefix(i.Scope, ".")
	if u, err := url.Parse(i.Scope); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	for site, v := range s.Sites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return v
		}
	}
	return ""
}
func (e *Engine) merge(source string, s *sourceState, snap Snapshot) error {
	// Preserve prior records unless a complete scope or explicit exclusion
	// authorizes revocation. Unsupported and held scopes cannot be mutated.
	next := desired{Generation: snap.Generation, Items: map[string]Item{}, Surfaces: map[string]string{}, Repair: s.Desired.Repair, Contexts: s.Desired.Contexts, OwnedDefaults: s.Desired.OwnedDefaults, PendingSince: s.Desired.PendingSince, LastAppliedAt: s.Desired.LastAppliedAt}
	if next.PendingSince.IsZero() {
		next.PendingSince = snap.ReceivedAt
		if next.PendingSince.IsZero() {
			next.PendingSince = e.now()
		}
	}
	maps.Copy(next.Surfaces, s.Desired.Surfaces)
	maps.Copy(next.Surfaces, snap.Surfaces)
	for id, i := range s.Desired.Items {
		state := scopeState(snap, i)
		site := siteState(snap, i)
		if state != "unsupported" && site != "held" && (site == "excluded" || state == "complete") {
			i.Deleted = true
			i.Generation = snap.Generation
		}
		next.Items[id] = i
	}
	for _, i := range snap.Items {
		if i.Surface == "" || i.Scope == "" || i.Key == "" || !json.Valid(i.Value) {
			return errors.New("invalid destination record")
		}
		state := scopeState(snap, i)
		site := siteState(snap, i)
		if state == "unsupported" || state == "incomplete" && i.Surface != "cookies" || site == "held" || site == "excluded" {
			continue
		}
		if e.ownedByOther(source, i.ID()) {
			next.Conflicts++
			continue
		}
		i.Generation = snap.Generation
		i.Deleted = false
		if i.Hash == "" {
			i.Hash = Hash(i.Value)
		}
		next.Items[i.ID()] = i
	}
	if err := e.fault("before-desired-save"); err != nil {
		return err
	}
	if err := e.write(filepath.Join(e.dir, source, "desired.json"), next); err != nil {
		return err
	}
	s.Desired = next
	return nil
}
func (e *Engine) Accept(ctx context.Context, source string, snap Snapshot) (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !validName(source) {
		return Status{}, errors.New("invalid source hostname")
	}
	if info, err := os.Lstat(filepath.Join(e.dir, source)); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return Status{}, errors.New("source state directory may not be a symlink")
	}
	if err := validateSnapshot(snap); err != nil {
		return Status{}, err
	}
	if snap.Generation == 0 {
		return Status{}, errors.New("snapshot generation must be positive")
	}
	s := e.sources[source]
	if s == nil {
		s = blankState()
		e.sources[source] = s
	}
	if snap.Generation <= s.Accepted.Generation {
		return e.status(s, nil), fmt.Errorf("stale generation %d (accepted %d)", snap.Generation, s.Accepted.Generation)
	}
	// accepted is the recovery journal for a crash before desired is committed.
	snap.ReceivedAt = e.now()
	if err := e.write(filepath.Join(e.dir, source, "accepted.json"), snap); err != nil {
		return Status{}, err
	}
	s.Accepted = snap
	if err := e.merge(source, s, snap); err != nil {
		return e.status(s, nil), err
	}
	return e.reconcile(ctx, source, s)
}
func (e *Engine) Reconcile(ctx context.Context, source string) (Status, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sources[source]
	if s == nil {
		return Status{}, errors.New("unknown source")
	}
	if s.Accepted.Generation > s.Desired.Generation {
		if err := e.merge(source, s, s.Accepted); err != nil {
			return Status{}, err
		}
	}
	return e.reconcile(ctx, source, s)
}
func (e *Engine) Sources() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.sources))
	for name := range e.sources {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func (e *Engine) Status(source string) (Status, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sources[source]
	if !ok {
		return Status{}, false
	}
	return e.status(s, nil), true
}
func (e *Engine) Repair(ctx context.Context, source string, confirmed bool) (Status, error) {
	if !confirmed {
		return Status{}, errors.New("repair requires operator confirmation: unidentified revoked items cannot be removed")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sources[source]
	if s == nil {
		return Status{}, errors.New("unknown source")
	}
	old := s.Desired.Repair
	st, err := e.reconcile(ctx, source, s)
	if err == nil && st.Running && len(st.Pending) == 0 && len(st.Failed) == 0 && st.Conflicts == 0 {
		s.Desired.Repair = nil
		if err = e.saveDesired(source, s); err != nil {
			s.Desired.Repair = old
		}
	}
	return e.finishTiming(source, s, e.status(s, st.Failed), err)

}
func (e *Engine) status(s *sourceState, failures []string) Status {
	st := Status{CapExcluded: s.Accepted.CapExcluded, PendingSince: s.Desired.PendingSince, LastAcceptedAt: s.Accepted.ReceivedAt, LastAppliedAt: s.Desired.LastAppliedAt, Running: len(s.Active) > 0, Accepted: s.Accepted.Generation, Applied: map[string]uint64{}, Pending: map[string]int{}, Failed: failures, Conflicts: s.Desired.Conflicts, RepairNeeded: s.Desired.Repair}
	var def *ledger
	for _, c := range s.Active {
		if c.Default {
			def = s.Ledgers[c.Key()]
			break
		}
	}
	if failures == nil {
		st.Failed = append([]string(nil), s.Failures...)
	}
	for surface, state := range s.Desired.Surfaces {
		if state == "unsupported" {
			continue
		}
		if def != nil {
			st.Applied[surface] = def.Applied[surface]
		}
		if st.Applied[surface] != s.Desired.Generation {
			st.Pending[surface]++
		}
	}
	for _, in := range s.Intents {
		if !in.Done {
			st.Pending[in.Item.Surface]++
		}
	}
	if st.PendingSince.IsZero() && s.Accepted.Generation > s.Desired.Generation {
		st.PendingSince = s.Accepted.ReceivedAt
	}
	if s.JournalError != nil {
		st.Failed = appendUnique(st.Failed, "intent journal write failed; reload required")
	}
	st.Ready = def != nil && s.Desired.Generation == s.Accepted.Generation && s.Accepted.Generation > 0 && st.RepairNeeded == nil && len(st.Pending) == 0 && len(st.Failed) == 0 && st.Conflicts == 0
	return st
}
func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Timing is durable state, not a per-poll observation. A stream of newer
// generations must not hide a destination that has remained pending for hours.
func (e *Engine) finishTiming(source string, s *sourceState, st Status, resultErr error) (Status, error) {
	pending, applied := s.Desired.PendingSince, s.Desired.LastAppliedAt
	if st.Ready {
		if !pending.IsZero() || applied.IsZero() {
			s.Desired.LastAppliedAt = e.now()
		}
		s.Desired.PendingSince = time.Time{}
	} else if s.Accepted.Generation > 0 && pending.IsZero() {
		s.Desired.PendingSince = e.now()
	}
	if pending != s.Desired.PendingSince || applied != s.Desired.LastAppliedAt {
		if err := e.saveDesired(source, s); err != nil {
			s.Desired.PendingSince = pending
			s.Desired.LastAppliedAt = applied
			s.Failures = appendUnique(s.Failures, "status timing persistence failed")
			st.Failed = appendUnique(st.Failed, "status timing persistence failed")
			st.Ready = false
			return st, errors.Join(resultErr, err)
		}
	}
	st.PendingSince = s.Desired.PendingSince
	st.LastAppliedAt = s.Desired.LastAppliedAt
	st.LastAcceptedAt = s.Accepted.ReceivedAt
	return st, resultErr
}

func (l *ledger) AppliedMax() uint64 {
	var n uint64
	for _, g := range l.Applied {
		if g > n {
			n = g
		}
	}
	return n
}
func (e *Engine) appendIntent(source string, s *sourceState, in intent) error {
	return e.appendIntents(source, s, []intent{in})
}
func (e *Engine) appendIntents(source string, s *sourceState, entries []intent) error {
	if len(entries) == 0 {
		return nil
	}
	if s.JournalError != nil {
		return s.JournalError
	}
	if err := e.appendLogs(filepath.Join(e.dir, source, "intents.log"), entries); err != nil {
		s.JournalError = fmt.Errorf("intent journal append uncertain; reload required: %w", err)
		return s.JournalError
	}
	// Publish the whole group only after successful file and directory sync.
	for _, in := range entries {
		s.Intents[in.ID] = in
	}
	s.LogEntries += len(entries)
	return nil
}
func cloneLedger(l *ledger) *ledger {
	return &ledger{Context: l.Context, Items: maps.Clone(l.Items), Applied: maps.Clone(l.Applied), Groups: maps.Clone(l.Groups)}
}
func (e *Engine) publishLedger(source string, s *sourceState, l *ledger) error {
	if err := e.saveLedger(source, l); err != nil {
		return err
	}
	s.Ledgers[l.Context.Key()] = l
	return nil
}
func (e *Engine) groupKey(i Item) string {
	if b, ok := e.applier.(BatchApplier); ok {
		if k := b.BatchKey(i); k != "" {
			return k
		}
	}
	return i.ID()
}

// settleSuperseded checkpoints verified ownership before abandoning old
// attempts. Recovery reads and durable boundaries are per group, not per key.
func (e *Engine) settleSuperseded(ctx context.Context, source string, s *sourceState, c Context, l *ledger) (*ledger, error) {
	groups := map[string][]intent{}
	for _, in := range s.Intents {
		if in.Done {
			continue
		}
		sameContext := in.Context.Key() == c.Key() || sameOwnedDefault(in.Context, c)
		if !sameContext && in.Context.Incarnation == c.Incarnation {
			continue
		}
		want, ok := s.Desired.Items[in.Item.ID()]
		if !ok || s.Desired.Surfaces[in.Item.Surface] == "unsupported" || want.Generation != in.Item.Generation || want.Deleted != in.Item.Deleted || want.Hash != in.Item.Hash || in.Context.Incarnation != c.Incarnation {
			key := e.groupKey(in.Item)
			groups[key] = append(groups[key], in)
		}
	}
	order := make([]string, 0, len(groups))
	for key := range groups {
		order = append(order, key)
	}
	sort.Strings(order)
	for _, key := range order {
		entries := groups[key]
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		var reads []Item
		for _, in := range entries {
			if !in.Item.Deleted && (in.Context.Key() == c.Key() || sameOwnedDefault(in.Context, c)) {
				reads = append(reads, in.Item)
			}
		}
		if p, ok := e.applier.(RecoveryPreparer); ok && len(reads) > 0 {
			if err := p.PrepareRecovery(ctx, c, reads); err != nil {
				return l, err
			}
		}
		next := cloneLedger(l)
		changed := false
		for index, in := range entries {
			if !in.Item.Deleted && (in.Context.Key() == c.Key() || sameOwnedDefault(in.Context, c)) {
				hash, exists, err := e.applier.Read(ctx, c, in.Item)
				if err != nil {
					return l, err
				}
				if exists && hash == in.Item.Hash {
					next.Items[in.Item.ID()] = in.Item
					changed = true
				}
			}
			in.Done = true
			in.Abandoned = true
			entries[index] = in
		}
		if changed {
			if err := e.publishLedger(source, s, next); err != nil {
				return l, err
			}
			l = next
		}
		if err := e.appendIntents(source, s, entries); err != nil {
			return l, err
		}
	}
	return l, nil
}
func (e *Engine) reconcile(ctx context.Context, source string, s *sourceState) (result Status, resultErr error) {
	defer func() { result, resultErr = e.finishTiming(source, s, result, resultErr) }()
	if s.JournalError != nil {
		return e.status(s, nil), s.JournalError
	}
	contexts, err := e.applier.Contexts(ctx)
	s.Active = contexts
	if err != nil {
		return e.status(s, []string{"browser unavailable"}), err
	}
	if len(contexts) == 0 {
		return e.status(s, []string{"no browser contexts"}), nil
	}
	failures := []string{}
	defer func() { s.Failures = append([]string(nil), failures...) }()
	for _, c := range contexts {
		if c.ID == "" || c.Incarnation == "" {
			return Status{}, errors.New("context identity and process incarnation required")
		}
		if a, ok := e.applier.(ReconcileApplier); ok {
			if err := a.BeginReconcile(ctx, c); err != nil {
				failures = append(failures, "context read failed")
				continue
			}
			defer a.EndReconcile(c)
		}
		l := s.Ledgers[c.Key()]
		if l == nil {
			l = &ledger{Context: c, Items: map[string]Item{}, Applied: map[string]uint64{}}
		} else {
			l = cloneLedger(l)
		}
		l.Context = c
		previous := s.Desired.OwnedDefaults[c.OwnedProfile]
		newOwnedDefault := c.Default && c.OwnedProfile != "" && previous != c.Key()
		if newOwnedDefault {
			if prior := s.Ledgers[previous]; prior != nil && sameOwnedDefault(prior.Context, c) {
				// Carry ownership evidence, never readiness. All records, including
				// logout tombstones, must be read back in this new incarnation.
				maps.Copy(l.Items, prior.Items)
			}
			if err := e.publishLedger(source, s, l); err != nil {
				return e.status(s, failures), err
			}
		}
		if s.Desired.Contexts == nil {
			s.Desired.Contexts = map[string]Context{}
		}
		_, known := s.Desired.Contexts[c.Key()]
		if !known || newOwnedDefault {
			s.Desired.Contexts[c.Key()] = c
			if newOwnedDefault {
				if s.Desired.OwnedDefaults == nil {
					s.Desired.OwnedDefaults = map[string]string{}
				}
				s.Desired.OwnedDefaults[c.OwnedProfile] = c.Key()
			}
			if err := e.saveDesired(source, s); err != nil {
				if !known {
					delete(s.Desired.Contexts, c.Key())
				}
				if newOwnedDefault {
					s.Desired.OwnedDefaults[c.OwnedProfile] = previous
				}
				return e.status(s, failures), err
			}
		}

		l, err = e.settleSuperseded(ctx, source, s, c, l)
		if err != nil {
			return e.status(s, []string{"unfinished intent recovery failed"}), err
		}
		ids := make([]string, 0, len(s.Desired.Items))
		for id := range s.Desired.Items {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		groups := map[string][]Item{}
		var order []string
		for _, id := range ids {
			i := s.Desired.Items[id]
			if s.Desired.Surfaces[i.Surface] == "unsupported" {
				continue
			}
			group := e.groupKey(i)
			if _, ok := groups[group]; !ok {
				order = append(order, group)
			}
			groups[group] = append(groups[group], i)
		}
		// A short time budget must not repeatedly service the same early
		// origin/database. New groups go first, followed by the oldest verified
		// generation, with a stable tie-breaker across restarts.
		sort.SliceStable(order, func(i, j int) bool {
			a, b := l.Groups[order[i]], l.Groups[order[j]]
			if a != b {
				return a < b
			}
			return order[i] < order[j]
		})
		pendingByItem := map[string][]intent{}
		for _, in := range s.Intents {
			if !in.Done && in.Context.Key() == c.Key() {
				id := in.Item.ID()
				pendingByItem[id] = append(pendingByItem[id], in)
			}
		}
		failedSurfaces := map[string]bool{}
		for _, group := range order {
			groupFailed := false
			var changes []Item
			var intents []intent
			var completed []intent
			for _, i := range groups[group] {
				// Only current durable ownership authorizes a tombstone. Completed
				// writes from before a successful deletion never authorize it again.
				if i.Deleted {
					if _, owned := l.Items[i.ID()]; !owned {
						continue
					}
				}
				hash, exists, readErr := e.applier.Read(ctx, c, i)
				if readErr != nil {
					groupFailed = true
					failedSurfaces[i.Surface] = true
					failures = append(failures, i.Surface+": read-back failed")
					continue
				}
				matched := i.Deleted && !exists || !i.Deleted && exists && hash == i.Hash
				if old, ok := l.Items[i.ID()]; matched && (ok && old.Generation == i.Generation && old.Hash == i.Hash && !i.Deleted || i.Deleted && !ok) {
					// Matching keys alone never establish ownership. This fast path is
					// only for a durable, already verified record (or confirmed absence).
					for _, pending := range pendingByItem[i.ID()] {
						pending.Done = true
						completed = append(completed, pending)
					}
					continue
				}
				// Even a matching value needs an intent before it can become owned.
				in := intent{ID: digest([]byte(c.Key() + i.ID() + fmt.Sprint(i.Generation, i.Deleted, i.Hash))), Context: c, Item: i, StartedAt: time.Now().UTC()}
				intents = append(intents, in)
				if !matched {
					changes = append(changes, i)
				}
			}
			if err := e.appendIntents(source, s, intents); err != nil {
				return e.status(s, failures), err
			}
			if len(intents) > 0 {
				if err := e.fault("after-intent"); err != nil {
					return e.status(s, failures), err
				}
			}
			if len(changes) > 0 {
				var writeErr error
				if b, ok := e.applier.(BatchApplier); ok && b.BatchKey(changes[0]) != "" {
					writeErr = b.ApplyBatch(ctx, c, changes)
				} else {
					for _, i := range changes {
						if i.Deleted {
							writeErr = e.applier.Delete(ctx, c, i)
						} else {
							writeErr = e.applier.Set(ctx, c, i)
						}
						if writeErr != nil {
							break
						}
					}
				}
				if err := e.fault("after-write"); err != nil {
					return e.status(s, failures), err
				}
				if writeErr != nil {
					for _, i := range changes {
						groupFailed = true
						failedSurfaces[i.Surface] = true
					}
					failures = append(failures, changes[0].Surface+": write failed")
				}
			}
			next := cloneLedger(l)
			for _, in := range intents {
				i := in.Item
				hash, exists, readErr := e.applier.Read(ctx, c, i)
				if readErr != nil || ((!i.Deleted || exists) && (i.Deleted || !exists || hash != i.Hash)) {
					groupFailed = true
					failedSurfaces[i.Surface] = true
					failures = appendUnique(failures, i.Surface+": value verification failed")
					continue
				}
				if i.Deleted {
					delete(next.Items, i.ID())
				} else {
					next.Items[i.ID()] = i
				}
				in.Done = true
				completed = append(completed, in)
			}
			groupAdvanced := !groupFailed && l.Groups[group] != s.Desired.Generation
			if groupAdvanced {
				if next.Groups == nil {
					next.Groups = map[string]uint64{}
				}
				next.Groups[group] = s.Desired.Generation
			}
			if len(completed) > 0 || groupAdvanced {
				if err := e.publishLedger(source, s, next); err != nil {
					return e.status(s, failures), err
				}
				l = next
				if err := e.fault("after-group-ledger"); err != nil {
					return e.status(s, failures), err
				}
			}
			if err := e.appendIntents(source, s, completed); err != nil {
				return e.status(s, failures), err
			}
			if len(completed) > 0 {
				if err := e.fault("after-group-completion"); err != nil {
					return e.status(s, failures), err
				}
			}
		}
		next := cloneLedger(l)
		for surface, state := range s.Desired.Surfaces {
			if state != "unsupported" && !failedSurfaces[surface] {
				next.Applied[surface] = s.Desired.Generation
			}
		}
		if !maps.Equal(next.Applied, l.Applied) || s.Ledgers[c.Key()] == nil {
			if err := e.publishLedger(source, s, next); err != nil {
				return e.status(s, failures), err
			}
		}
	}
	if err := e.compact(source, s); err != nil {
		return e.status(s, failures), err
	}
	return e.status(s, failures), nil
}
func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

func validateSnapshot(s Snapshot) error {
	if s.CapExcluded < 0 {
		return errors.New("invalid excluded-record count")
	}
	if s.Generation == 0 {
		return errors.New("snapshot generation must be positive")
	}
	validStatus := func(v string) bool { return v == "complete" || v == "incomplete" || v == "unsupported" }
	for surface, state := range s.Surfaces {
		if surface != "cookies" && surface != "local_storage" && surface != "indexed_db" {
			return fmt.Errorf("unsupported snapshot surface %q", surface)
		}
		if !validStatus(state) {
			return fmt.Errorf("invalid surface status %q", state)
		}
	}
	for _, state := range s.Scopes {
		if !validStatus(state) {
			return fmt.Errorf("invalid scope status %q", state)
		}
	}
	for _, state := range s.Sites {
		if state != "held" && state != "excluded" && state != "shipped" {
			return fmt.Errorf("invalid site status %q", state)
		}
	}
	ids := map[string]bool{}
	for _, i := range s.Items {
		if i.Surface == "" || i.Scope == "" || i.Key == "" || !json.Valid(i.Value) {
			return errors.New("invalid destination item")
		}
		if _, ok := s.Surfaces[i.Surface]; !ok {
			return errors.New("item surface status missing")
		}
		if ids[i.ID()] {
			return errors.New("duplicate destination item")
		}
		ids[i.ID()] = true
		if i.Hash != "" {
			if b, err := hex.DecodeString(i.Hash); err != nil || len(b) != 32 {
				return errors.New("invalid value hash")
			}
		}
	}
	return nil
}

func (e *Engine) compact(source string, s *sourceState) error {
	if s.LogEntries < max(1024, 2*len(s.Desired.Items)*max(1, len(s.Desired.Contexts))) {
		return nil
	}
	latest := map[string]intent{}
	for _, in := range s.Intents {
		k := in.Context.Key() + in.Item.ID()
		old, ok := latest[k]
		if !ok || old.Item.Generation < in.Item.Generation || old.Item.Generation == in.Item.Generation && old.StartedAt.Before(in.StartedAt) {
			latest[k] = in
		}
	}
	logs := make([]intent, 0, len(latest))
	for _, in := range latest {
		logs = append(logs, in)
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].StartedAt.Before(logs[j].StartedAt) })
	if err := e.replaceLog(filepath.Join(e.dir, source, "intents.log"), logs); err != nil {
		return err
	}
	s.Intents = map[string]intent{}
	s.LogEntries = len(logs)
	for _, in := range logs {
		s.Intents[in.ID] = in
	}
	return nil
}

// FilterSnapshot enforces destination policy for both incoming and previously
// owned records. Exclusions are explicit so incomplete reads cannot retain an
// account the operator has disabled.
func (e *Engine) FilterSnapshot(s *Snapshot, allowed func(string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.Sites == nil {
		s.Sites = map[string]string{}
	}
	check := func(i Item) bool {
		host := i.Scope
		if u, err := url.Parse(i.Scope); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		if !allowed(host) {
			s.Sites[i.Scope] = "excluded"
			return false
		}
		return true
	}
	for _, state := range e.sources {
		for _, i := range state.Desired.Items {
			check(i)
		}
	}
	kept := s.Items[:0]
	for _, i := range s.Items {
		if check(i) {
			kept = append(kept, i)
		}
	}
	s.Items = kept
}

// ValidateSnapshot is the ingress guard; call before advancing replay state.
func ValidateSnapshot(s Snapshot) error { return validateSnapshot(s) }
