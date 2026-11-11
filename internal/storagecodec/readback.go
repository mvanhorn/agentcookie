package storagecodec

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type readbackValue struct {
	Hash   string
	Exists bool
	Error  error
}
type readbackStore struct {
	Name json.RawMessage   `json:"name"`
	Keys []json.RawMessage `json:"keys"`
}
type readbackPlan struct {
	Context, Origin string
	Database        *protocol.IDBDatabase
	Stores          []readbackStore
	Items           [][]destination.Item
	Schemas         []destination.Item
	Values          map[string]readbackValue
	IDs             map[string]bool
	Error           error
}

// BatchReadRunner opts into one bounded readonly transaction per database,
// rather than opening a hidden target for each key. Custom runners may omit it.
type BatchReadRunner interface{ BatchReads() bool }

func (r *remoteRunner) BatchReads() bool { return true }

func (a *Adapter) prepareReadback(c destination.Context, items []destination.Item) error {
	a.mu.Lock()
	a.readback = nil
	a.mu.Unlock()
	runner, ok := a.Runner.(BatchReadRunner)
	if !ok || !runner.BatchReads() {
		return nil
	}
	plan := &readbackPlan{Context: c.Key(), IDs: map[string]bool{}, Stores: []readbackStore{}, Items: [][]destination.Item{}}
	indices := map[string]int{}
	var generation uint64
	for _, item := range items {
		if a.BatchKey(item) != a.BatchKey(items[0]) {
			return errors.New("mixed readback database scopes")
		}
		p, err := payload(item)
		if err != nil {
			return err
		}
		if plan.Origin != "" && plan.Origin != p.Origin {
			return errors.New("mixed readback origins")
		}
		plan.Origin = p.Origin
		plan.IDs[item.ID()] = true
		if p.Kind == "schema" {
			plan.Schemas = append(plan.Schemas, item)
			if plan.Database == nil || !item.Deleted && item.Generation >= generation {
				plan.Database = p.Database
				generation = item.Generation
			}
			continue
		}
		if plan.Database == nil {
			plan.Database = &protocol.IDBDatabase{Name: p.DatabaseName, Version: p.DatabaseVersion}
			if p.Database != nil {
				plan.Database.Name = p.Database.Name
				plan.Database.Version = p.Database.Version
			}
		}
		key := string(p.Store)
		index, found := indices[key]
		if !found {
			index = len(plan.Stores)
			indices[key] = index
			plan.Stores = append(plan.Stores, readbackStore{Name: p.Store})
			plan.Items = append(plan.Items, nil)
		}
		plan.Stores[index].Keys = append(plan.Stores[index].Keys, p.Key)
		plan.Items[index] = append(plan.Items[index], item)
	}
	if plan.Database == nil {
		return errors.New("readback database metadata missing")
	}
	if _, err := JSONSize(map[string]any{"database": plan.Database, "stores": plan.Stores}, protocol.MaxSurfaceBytes); err != nil {
		return err
	}
	a.mu.Lock()
	a.readback = plan
	a.mu.Unlock()
	return nil
}
func (a *Adapter) invalidateReadback() {
	a.mu.Lock()
	if a.readback != nil {
		a.readback.Values = nil
		a.readback.Error = nil
	}
	a.mu.Unlock()
}
func (a *Adapter) cachedRead(ctx context.Context, c destination.Context, item destination.Item) (string, bool, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	plan := a.readback
	if plan == nil || plan.Context != c.Key() || !plan.IDs[item.ID()] {
		return "", false, false, nil
	}
	if plan.Error != nil {
		return "", false, true, plan.Error
	}
	fail := func(err error) (string, bool, bool, error) { plan.Error = err; return "", false, true, err }
	if plan.Values == nil {
		var result struct {
			Schema bool `json:"schema"`
			Stores [][]struct {
				Absent bool            `json:"absent"`
				Error  string          `json:"error"`
				Value  json.RawMessage `json:"value"`
			} `json:"stores"`
		}
		if err := a.run(ctx, c, plan.Origin, "readIndexedDBBatch", map[string]any{"database": plan.Database, "stores": plan.Stores}, &result); err != nil {
			return fail(err)
		}
		if len(result.Stores) != len(plan.Items) {
			return fail(errors.New("invalid readback store count"))
		}
		values := map[string]readbackValue{}
		for n, items := range plan.Items {
			if len(result.Stores[n]) != len(items) {
				return fail(errors.New("invalid readback record count"))
			}
			for k, item := range items {
				v := result.Stores[n][k]
				if v.Error != "" {
					values[item.ID()] = readbackValue{Error: errors.New("record readback: " + v.Error)}
					continue
				}
				if !v.Absent {
					if err := Validate(v.Value); err != nil {
						values[item.ID()] = readbackValue{Error: err}
						continue
					}
				}
				values[item.ID()] = readbackValue{Hash: destination.Hash(v.Value), Exists: !v.Absent}
			}
		}
		for _, item := range plan.Schemas {
			values[item.ID()] = readbackValue{Hash: item.Hash, Exists: result.Schema && !item.Deleted}
		}
		plan.Values = values
	}
	v := plan.Values[item.ID()]
	return v.Hash, v.Exists, true, v.Error
}

// PrepareRecovery reads prior intent keys using only their recorded identities;
// it does not resolve current schema references or mutate browser storage.
func (a *Adapter) PrepareRecovery(ctx context.Context, c destination.Context, items []destination.Item) error {
	if len(items) == 0 {
		return nil
	}
	if items[0].Surface != "indexed_db" {
		if base, ok := a.Base.(interface {
			PrepareRecovery(context.Context, destination.Context, []destination.Item) error
		}); ok {
			return base.PrepareRecovery(ctx, c, items)
		}
		return nil
	}
	records := make([]destination.Item, 0, len(items))
	for _, item := range items {
		p, err := payload(item)
		if err != nil {
			return err
		}
		if p.Kind != "schema" {
			records = append(records, item)
		}
	}
	if len(records) == 0 {
		a.mu.Lock()
		a.readback = nil
		a.mu.Unlock()
		return nil
	}
	return a.prepareReadback(c, records)
}
