package storagecodec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

// Runner abstracts CDP for deterministic intent/transaction tests.
type Runner interface {
	Run(context.Context, cdp.BrowserContextID, string, string, map[string]any, any) error
}
type StoragePayload struct {
	Origin          string                `json:"origin"`
	Kind            string                `json:"kind"`
	Database        *protocol.IDBDatabase `json:"database,omitempty"`
	Store           json.RawMessage       `json:"store,omitempty"`
	Key             json.RawMessage       `json:"key,omitempty"`
	Value           json.RawMessage       `json:"value,omitempty"`
	SchemaID        string                `json:"schema_id,omitempty"`
	DatabaseName    json.RawMessage       `json:"database_name,omitempty"`
	DatabaseVersion uint64                `json:"database_version,omitempty"`
}

// Adapter routes non-storage surfaces to Base. BatchKey groups all IDB writes
// in one database so the durable engine journals all intents before one atomic
// transaction. Read verifies values from a per-database readonly snapshot;
// every mutation invalidates that snapshot before verification.
type Adapter struct {
	Base        destination.Applier
	Runner      Runner
	Diagnostic  func(string)
	mu          sync.Mutex
	diagnostics []string
	schemas     map[string]*protocol.IDBDatabase
	readback    *readbackPlan
}

// Diagnostics reports schema-version and generator consequences without values.
func (a *Adapter) Diagnostics() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.diagnostics)
}
func (a *Adapter) diagnose(message string) {
	a.mu.Lock()
	if len(a.diagnostics) >= 100 {
		a.diagnostics = a.diagnostics[1:]
	}
	a.diagnostics = append(a.diagnostics, message)
	a.mu.Unlock()
	if a.Diagnostic != nil {
		a.Diagnostic(message)
	}
}

// PrepareBatch reconstructs one bounded schema registry from durable sentinels.
// Record payloads carry a schema hash, never N copies of the database schema.
func (a *Adapter) PrepareBatch(ctx context.Context, c destination.Context, items []destination.Item) error {
	if len(items) == 0 || items[0].Surface != string(protocol.IndexedDB) {
		if base, ok := a.Base.(destination.BatchPreparer); ok {
			return base.PrepareBatch(ctx, c, items)
		}
		return nil
	}
	schemas := map[string]*protocol.IDBDatabase{}
	used := 0
	for _, item := range items {
		if item.Key != "$schema" {
			continue
		}
		p, err := payload(item)
		if err != nil {
			return err
		}
		if p.Database == nil {
			return errors.New("missing durable schema")
		}
		size, err := JSONSize(p.Database, protocol.MaxDatabaseBytes-used)
		if err != nil {
			return err
		}
		used += size
		raw, _ := json.Marshal(p.Database)
		id := destination.Hash(raw)
		if p.SchemaID != "" && p.SchemaID != id {
			return errors.New("schema reference mismatch")
		}
		schemas[id] = p.Database
	}
	a.mu.Lock()
	a.schemas = schemas
	a.mu.Unlock()
	return a.prepareReadback(c, items)
}
func (a *Adapter) resolve(p StoragePayload, deleted bool) (StoragePayload, error) {
	if p.Database != nil {
		return p, nil
	}
	if len(p.DatabaseName) == 0 || p.DatabaseVersion == 0 {
		return p, errors.New("missing compact database identity")
	}
	if deleted {
		p.Database = &protocol.IDBDatabase{Name: p.DatabaseName, Version: p.DatabaseVersion, Stores: []protocol.IDBStore{}}
		return p, nil
	}
	a.mu.Lock()
	db := a.schemas[p.SchemaID]
	a.mu.Unlock()
	if db == nil || destination.Hash(db.Name) != destination.Hash(p.DatabaseName) {
		return p, errors.New("durable schema reference unavailable")
	}
	p.Database = db
	return p, nil
}

func (a *Adapter) BeginReconcile(ctx context.Context, c destination.Context) error {
	if base, ok := a.Base.(destination.ReconcileApplier); ok {
		return base.BeginReconcile(ctx, c)
	}
	return nil
}
func (a *Adapter) EndReconcile(c destination.Context) {
	a.mu.Lock()
	a.readback = nil
	a.mu.Unlock()
	if base, ok := a.Base.(destination.ReconcileApplier); ok {
		base.EndReconcile(c)
	}
}

func (a *Adapter) Contexts(ctx context.Context) ([]destination.Context, error) {
	if a.Base == nil {
		return nil, errors.New("missing context provider")
	}
	return a.Base.Contexts(ctx)
}
func browserContext(c destination.Context) cdp.BrowserContextID {
	if c.Default {
		return ""
	}
	return cdp.BrowserContextID(c.ID)
}

func isStorage(i destination.Item) bool {
	return i.Surface == string(protocol.LocalStorage) || i.Surface == string(protocol.IndexedDB)
}
func payload(i destination.Item) (StoragePayload, error) {
	var p StoragePayload
	if len(i.Value) > protocol.MaxDatabaseBytes {
		return p, errors.New("storage payload bytes limit")
	}
	if err := json.Unmarshal(i.Value, &p); err != nil {
		return p, err
	}
	if err := ValidOrigin(p.Origin); err != nil {
		return p, err
	}
	return p, nil
}
func (a *Adapter) run(ctx context.Context, c destination.Context, origin, operation string, args map[string]any, out any) error {
	if runner, ok := a.Runner.(interface {
		RunContext(context.Context, destination.Context, string, string, map[string]any, any) error
	}); ok {
		return runner.RunContext(ctx, c, origin, operation, args, out)
	}
	return a.Runner.Run(ctx, browserContext(c), origin, operation, args, out)
}

func (a *Adapter) Read(ctx context.Context, c destination.Context, i destination.Item) (string, bool, error) {
	if !isStorage(i) {
		return a.Base.Read(ctx, c, i)
	}
	p, err := payload(i)
	if err != nil {
		return "", false, err
	}
	if a.Runner == nil {
		return "", false, errors.New("missing storage runner")
	}
	if p.Kind == "schema" && i.Deleted {
		return "", false, nil
	} // Additive schema is never physically deleted.
	if i.Surface == string(protocol.IndexedDB) {
		if hash, exists, handled, err := a.cachedRead(ctx, c, i); handled {
			return hash, exists, err
		}
	}
	args := map[string]any{"key": p.Key, "deleted": i.Deleted}
	op := "readLocalStorage"
	if i.Surface == string(protocol.IndexedDB) {
		p, err = a.resolve(p, p.Kind != "schema" || i.Deleted)
		if err != nil {
			return "", false, err
		}
		op = "readIndexedDB"
		args["database"] = p.Database
		args["store"] = p.Store
		if p.Kind == "schema" {
			op = "readSchema"
		}
	}
	var result struct {
		Absent bool            `json:"absent"`
		Value  json.RawMessage `json:"value"`
	}
	if err := a.run(ctx, c, p.Origin, op, args, &result); err != nil {
		return "", false, err
	}
	if result.Absent {
		return "", false, nil
	}
	if len(result.Value) == 0 {
		return "", false, errors.New("missing storage read-back value")
	}
	return destination.Hash(result.Value), true, nil
}
func (a *Adapter) Set(ctx context.Context, c destination.Context, i destination.Item) error {
	if !isStorage(i) {
		return a.Base.Set(ctx, c, i)
	}
	if i.Surface == string(protocol.IndexedDB) {
		return a.ApplyBatch(ctx, c, []destination.Item{i})
	}
	return a.local(ctx, c, i, "set")
}
func (a *Adapter) Delete(ctx context.Context, c destination.Context, i destination.Item) error {
	if !isStorage(i) {
		return a.Base.Delete(ctx, c, i)
	}
	if i.Surface == string(protocol.IndexedDB) {
		i.Deleted = true
		return a.ApplyBatch(ctx, c, []destination.Item{i})
	}
	return a.local(ctx, c, i, "delete")
}
func (a *Adapter) local(ctx context.Context, c destination.Context, i destination.Item, op string) error {
	p, err := payload(i)
	if err != nil {
		return err
	}
	return a.run(ctx, c, p.Origin, "localStorage", map[string]any{"op": op, "key": p.Key, "value": p.Value}, nil)
}
func (a *Adapter) BatchKey(i destination.Item) string {
	if i.Surface == string(protocol.IndexedDB) {
		return i.Surface + "\x00" + i.Scope
	}
	if b, ok := a.Base.(destination.BatchApplier); ok {
		return b.BatchKey(i)
	}
	return ""
}
func (a *Adapter) ApplyBatch(ctx context.Context, c destination.Context, items []destination.Item) error {
	if len(items) == 0 {
		return nil
	}
	if items[0].Surface != string(protocol.IndexedDB) {
		if b, ok := a.Base.(destination.BatchApplier); ok {
			return b.ApplyBatch(ctx, c, items)
		}
		return errors.New("unsupported batch")
	}
	defer a.invalidateReadback()
	var selected *StoragePayload
	var newest uint64
	hasWrites := false
	hasRecordWrites := false
	for _, i := range items {
		if a.BatchKey(i) != a.BatchKey(items[0]) {
			return errors.New("mixed IDB batch")
		}
		p, err := payload(i)
		if err != nil {
			return err
		}
		p, err = a.resolve(p, i.Deleted)
		if err != nil {
			return err
		}
		if p.Kind == "schema" && i.Deleted {
			continue
		}
		if !i.Deleted {
			hasWrites = true
			if p.Kind != "schema" {
				hasRecordWrites = true
			}
		}
		if selected == nil || !i.Deleted && i.Generation >= newest {
			copy := p
			selected = &copy
			newest = i.Generation
		}
	}
	if selected == nil {
		return nil
	}
	encoded, _ := json.Marshal(selected.Database)
	var db protocol.IDBDatabase
	if err := json.Unmarshal(encoded, &db); err != nil {
		return err
	}
	stores := map[string]int{}
	for n := range db.Stores {
		db.Stores[n].Records = []protocol.IDBRecord{}
		stores[destination.Hash(db.Stores[n].Name)] = n
	}
	var deletes []map[string]any
	for _, i := range items {
		p, err := payload(i)
		if err != nil {
			return err
		}
		if p.Origin != selected.Origin {
			return errors.New("mixed IDB origins")
		}
		if p.Kind == "schema" {
			continue
		}
		if i.Deleted {
			deletes = append(deletes, map[string]any{"store": p.Store, "key": p.Key})
			continue
		}
		n, ok := stores[destination.Hash(p.Store)]
		if !ok {
			return errors.New("record store missing from schema")
		}
		db.Stores[n].Records = append(db.Stores[n].Records, protocol.IDBRecord{Key: p.Key, Value: p.Value})
	}
	var report struct {
		Schema       string `json:"schema"`
		GeneratorGap bool   `json:"generator_gap"`
	}
	deletesOnly := !hasWrites || len(deletes) > 0 && !hasRecordWrites
	if deletesOnly {
		db.Stores = []protocol.IDBStore{}
	}
	if err := a.run(ctx, c, selected.Origin, "indexedDB", map[string]any{"database": &db, "deletes": deletes, "deletes_only": deletesOnly}, &report); err != nil {
		return err
	}
	if strings.HasPrefix(report.Schema, "upgraded to ") {
		a.diagnose("storage " + selected.Origin + ": schema " + report.Schema + "; lower fixed-version opens may fail with VersionError")
	}
	if report.GeneratorGap {
		a.diagnose("storage " + selected.Origin + ": destination key generator is lower than source or could not be verified")
	}
	return nil
}

// AddRecords translates source records into durable per-key ownership. Schema
// sentinels preserve empty stores/indexes; their revocation relinquishes ownership
// without dropping a database, store, index, or another source's data.
func AddRecords(snapshot *destination.Snapshot, records *protocol.StorageRecords) error {
	if records == nil {
		return nil
	}
	if records.Version != protocol.StorageIRVersion {
		return errors.New("unsupported storage IR version")
	}
	if snapshot.Scopes == nil {
		snapshot.Scopes = map[string]string{}
	}
	expanded := 0
	add := func(surface, scope, key string, p StoragePayload, hashValue []byte) error {
		size, err := JSONSize(p, (512<<20)-expanded)
		if err != nil {
			return errors.New("expanded storage state bytes limit")
		}
		expanded += size + len(scope) + len(key) + 256
		if expanded > 512<<20 {
			return errors.New("expanded storage state bytes limit")
		}
		value, err := json.Marshal(p)
		if err != nil {
			return err
		}
		item := destination.NewItem(surface, scope, key, value)
		item.Hash = destination.Hash(hashValue)
		item.SharedMetadata = p.Kind == "schema"
		snapshot.Items = append(snapshot.Items, item)
		return nil
	}
	var lsBytes, idbBytes int
	seenOrigins := map[string]bool{}
	for _, o := range records.Origins {
		if seenOrigins[o.Origin] {
			return errors.New("multiple routed storage origins")
		}
		seenOrigins[o.Origin] = true
		lsWire, err := json.Marshal(o.LocalStorage)
		if err != nil {
			return err
		}
		lsBytes += len(lsWire)
		idbWire, err := json.Marshal(o.IndexedDB)
		if err != nil {
			return err
		}
		idbBytes += len(idbWire)
		if lsBytes > protocol.MaxSurfaceBytes || idbBytes > protocol.MaxSurfaceBytes {
			return errors.New("storage surface bytes limit")
		}
		if err := ValidOrigin(o.Origin); err != nil {
			return err
		}
		snapshot.Scopes[string(protocol.LocalStorage)+"\x00"+o.Origin] = o.LocalStorageStatus.State
		snapshot.Scopes[string(protocol.IndexedDB)+"\x00"+o.Origin] = o.IndexedDBStatus.State
		for _, ls := range o.LocalStorage {
			if err := ValidateString(ls.Key); err != nil {
				return err
			}
			if err := ValidateString(ls.Value); err != nil {
				return err
			}
			units, err := StringUnits(ls.Value)
			if err != nil {
				return err
			}
			if units*2 > protocol.MaxLocalStorageValueBytes {
				return errors.New("localStorage value bytes limit")
			}
			if err := add(string(protocol.LocalStorage), o.Origin, destination.Hash(ls.Key), StoragePayload{Origin: o.Origin, Kind: "record", Key: ls.Key, Value: ls.Value}, ls.Value); err != nil {
				return err
			}
		}
		for _, database := range o.IndexedDB {
			if database.Version == 0 || database.Version > 9007199254740991 {
				return errors.New("invalid database version")
			}
			dbWire, err := json.Marshal(database)
			if err != nil {
				return err
			}
			if len(dbWire) > protocol.MaxDatabaseBytes {
				return errors.New("database bytes limit")
			}
			if err := ValidateString(database.Name); err != nil {
				return err
			}
			scope := o.Origin + "/.agentcookie-db/" + destination.Hash(database.Name)
			snapshot.Scopes[string(protocol.IndexedDB)+"\x00"+scope] = database.Status.State
			schema := database
			schema.Stores = append([]protocol.IDBStore{}, database.Stores...)
			for n := range schema.Stores {
				schema.Stores[n].Records = nil
			}
			schema.Status = protocol.SurfaceStatus{}
			rawSchema, err := json.Marshal(schema)
			if err != nil {
				return err
			}
			if len(rawSchema) > protocol.MaxDatabaseBytes {
				return errors.New("schema bytes limit")
			}
			schemaID := destination.Hash(rawSchema)
			if err := add(string(protocol.IndexedDB), scope, "$schema", StoragePayload{Origin: o.Origin, Kind: "schema", Database: &schema, SchemaID: schemaID}, rawSchema); err != nil {
				return err
			}
			size := len(rawSchema)
			for _, s := range database.Stores {
				if s.Generator != nil && (math.IsNaN(*s.Generator) || math.IsInf(*s.Generator, 0) || *s.Generator < 1 || *s.Generator > 9007199254740992) {
					return errors.New("invalid key generator")
				}
				if string(s.KeyPath) != "null" {
					if err := Validate(s.KeyPath); err != nil {
						return err
					}
				}
				if err := ValidateString(s.Name); err != nil {
					return err
				}
				for _, idx := range s.Indexes {
					if err := ValidateString(idx.Name); err != nil {
						return err
					}
				}
				for _, r := range s.Records {
					recordWire, err := json.Marshal(r)
					if err != nil {
						return err
					}
					if len(recordWire) > protocol.MaxRecordBytes {
						return errors.New("record bytes limit")
					}
					if err := ValidateKey(r.Key); err != nil {
						return fmt.Errorf("IDB key: %w", err)
					}
					if err := Validate(r.Value); err != nil {
						return fmt.Errorf("IDB value: %w", err)
					}
					size += len(r.Key) + len(r.Value)
					if size > protocol.MaxDatabaseBytes {
						return errors.New("database bytes limit")
					}
					identity, _ := json.Marshal([]json.RawMessage{s.Name, r.Key})
					if err := add(string(protocol.IndexedDB), scope, destination.Hash(identity), StoragePayload{Origin: o.Origin, Kind: "record", SchemaID: schemaID, DatabaseName: schema.Name, DatabaseVersion: schema.Version, Store: s.Name, Key: r.Key, Value: r.Value}, r.Value); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
