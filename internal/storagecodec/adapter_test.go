package storagecodec

import (
	"fmt"
	"strings"

	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type fixtureBase struct{}

func (fixtureBase) Contexts(context.Context) ([]destination.Context, error) {
	return []destination.Context{{ID: "default", Incarnation: "fixture", Default: true}}, nil
}
func (fixtureBase) Read(context.Context, destination.Context, destination.Item) (string, bool, error) {
	return "", false, errors.New("unexpected base read")
}
func (fixtureBase) Set(context.Context, destination.Context, destination.Item) error {
	return errors.New("unexpected base set")
}
func (fixtureBase) Delete(context.Context, destination.Context, destination.Item) error {
	return errors.New("unexpected base delete")
}

type memoryRunner struct {
	values   map[string]json.RawMessage
	schemas  map[string]protocol.IDBDatabase
	batches  int
	failLS   int
	lsWrites int
}

func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func (r *memoryRunner) Run(_ context.Context, _ cdp.BrowserContextID, origin, op string, args map[string]any, out any) error {
	result := map[string]any{"absent": true}
	key := origin + ":" + destination.Hash(raw(args["key"]))
	switch op {
	case "localStorage":
		r.lsWrites++
		if r.failLS == r.lsWrites {
			return errors.New("QuotaExceededError")
		}
		if args["op"] == "delete" {
			delete(r.values, key)
		} else {
			r.values[key] = raw(args["value"])
		}
	case "readLocalStorage":
		if v, ok := r.values[key]; ok {
			result = map[string]any{"absent": false, "value": v}
		}
	case "readSchema":
		db := args["database"].(*protocol.IDBDatabase)
		if _, ok := r.schemas[destination.Hash(db.Name)]; ok {
			result = map[string]any{"absent": false, "value": db}
		}
	case "indexedDB":
		r.batches++
		db := args["database"].(*protocol.IDBDatabase)
		r.schemas[destination.Hash(db.Name)] = *db
		for _, s := range db.Stores {
			for _, v := range s.Records {
				r.values[origin+":"+destination.Hash(db.Name)+":"+destination.Hash(s.Name)+":"+destination.Hash(v.Key)] = v.Value
			}
		}
		for _, d := range args["deletes"].([]map[string]any) {
			delete(r.values, origin+":"+destination.Hash(db.Name)+":"+destination.Hash(raw(d["store"]))+":"+destination.Hash(raw(d["key"])))
		}
	case "readIndexedDB":
		db := args["database"].(*protocol.IDBDatabase)
		key = origin + ":" + destination.Hash(db.Name) + ":" + destination.Hash(raw(args["store"])) + ":" + destination.Hash(raw(args["key"]))
		if v, ok := r.values[key]; ok {
			result = map[string]any{"absent": false, "value": v}
		}
	default:
		return errors.New("unexpected operation " + op)
	}
	if out != nil {
		return json.Unmarshal(raw(result), out)
	}
	return nil
}
func str(s string) json.RawMessage { return raw(map[string]string{"t": "str", "v": s}) }
func recordsFixture() *protocol.StorageRecords {
	store := protocol.IDBStore{Name: str("tokens"), KeyPath: json.RawMessage("null"), Records: []protocol.IDBRecord{{Key: str("a"), Value: str("v1")}, {Key: str("b"), Value: str("v1")}}}
	db := protocol.IDBDatabase{Name: str("db"), Version: 1, Status: protocol.SurfaceStatus{State: protocol.Complete}, Stores: []protocol.IDBStore{store}}
	origin := protocol.StorageOrigin{Origin: "https://example.test", LocalStorageStatus: protocol.SurfaceStatus{State: protocol.Complete}, IndexedDBStatus: protocol.SurfaceStatus{State: protocol.Complete}, LocalStorage: []protocol.LocalStorageItem{{Key: str("token"), Value: str("v1")}}, IndexedDB: []protocol.IDBDatabase{db}}
	return &protocol.StorageRecords{Version: 1, Origins: []protocol.StorageOrigin{origin}}
}
func storageSnapshot(t *testing.T, g uint64, r *protocol.StorageRecords) destination.Snapshot {
	t.Helper()
	s := destination.Snapshot{Generation: g, Surfaces: map[string]string{"local_storage": "complete", "indexed_db": "complete"}}
	if err := AddRecords(&s, r); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestStorageEngineBatchHashesAndOwnership(t *testing.T) {
	r := &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}
	a := &Adapter{Base: fixtureBase{}, Runner: r}
	engine, err := destination.Open(t.TempDir(), nil, a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	records := recordsFixture()
	s := storageSnapshot(t, 1, records)
	status, err := engine.Accept(ctx, "source", s)
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if r.batches != 1 {
		t.Fatalf("want one database transaction, got %d", r.batches)
	}
	records.Origins[0].IndexedDB[0].Stores[0].Records = records.Origins[0].IndexedDB[0].Stores[0].Records[:1]
	records.Origins[0].IndexedDB[0].Stores[0].Records[0].Value = str("v2")
	records.Origins[0].LocalStorage[0].Value = str("v2")
	status, err = engine.Accept(ctx, "source", storageSnapshot(t, 2, records))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if r.batches != 2 {
		t.Fatal("record writes/deletes not batched")
	}
	if len(r.values) != 2 {
		t.Fatal("owned deletion did not remove one record", r.values)
	}
	status, err = engine.Accept(ctx, "other", storageSnapshot(t, 1, records))
	if err != nil {
		t.Fatal(err)
	}
	if status.Conflicts != 2 {
		t.Fatalf("only the two records should conflict: %+v", status)
	}
}
func TestStoragePartialQuotaPreservesFirstItem(t *testing.T) {
	r := &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}, failLS: 2}
	engine, err := destination.Open(t.TempDir(), nil, &Adapter{Base: fixtureBase{}, Runner: r})
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	records.Origins[0].IndexedDB = nil
	records.Origins[0].LocalStorage = append(records.Origins[0].LocalStorage, protocol.LocalStorageItem{Key: str("second"), Value: str("two")})
	status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records))
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || len(r.values) != 1 || len(status.Failed) == 0 {
		t.Fatal(status, r.values)
	}
	status, err = engine.Reconcile(context.Background(), "source")
	if err != nil || !status.Ready || len(r.values) != 2 {
		t.Fatal(status, err)
	}
}
func TestStorageScopeIncompleteAndSchemaSentinel(t *testing.T) {
	records := recordsFixture()
	records.Origins[0].IndexedDB[0].Stores[0].Records = nil
	records.Origins[0].IndexedDB[0].Status.State = protocol.Incomplete
	s := storageSnapshot(t, 1, records)
	if len(s.Items) != 2 {
		t.Fatal("empty schema missing", s.Items)
	}
	if s.Scopes["indexed_db\x00"+s.Items[1].Scope] != protocol.Incomplete {
		t.Fatal(s.Scopes)
	}
	records.Origins[0].LocalStorage[0].Value = json.RawMessage(`{"t":"str","v":"\ud800"}`)
	if AddRecords(&s, records) == nil {
		t.Fatal("corrupt UTF16 accepted")
	}
}
func TestCompactSchemaDoesNotMultiplyWithRecordCount(t *testing.T) {
	records := recordsFixture()
	store := &records.Origins[0].IndexedDB[0].Stores[0]
	store.Indexes = []protocol.IDBIndex{{Name: str(strings.Repeat("index", 1000)), KeyPath: str("value")}}
	store.Records = nil
	for i := range 500 {
		store.Records = append(store.Records, protocol.IDBRecord{Key: str(fmt.Sprint(i)), Value: str("token")})
	}
	snapshot := storageSnapshot(t, 1, records)
	size := 0
	schemaCount := 0
	for _, item := range snapshot.Items {
		size += len(item.Value)
		if item.Surface != "indexed_db" {
			continue
		}
		p, err := payload(item)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kind == "schema" {
			schemaCount++
			if p.Database == nil {
				t.Fatal("missing durable schema")
			}
		} else if p.Database != nil || p.SchemaID == "" {
			t.Fatal("record duplicated full schema")
		}
	}
	if schemaCount != 1 || size > 400000 {
		t.Fatalf("schema amplification: %d bytes, %d schemas", size, schemaCount)
	}
}
func TestCompleteOriginRevokesAbsentDatabaseDespiteOtherIncompleteOrigin(t *testing.T) {
	runner := &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}
	a := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, a)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	other := records.Origins[0]
	other.Origin = "https://other.test"
	records.Origins = append(records.Origins, other)
	if _, err = engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records)); err != nil {
		t.Fatal(err)
	}
	records.Origins[0].IndexedDB = nil
	records.Origins[1].IndexedDB = nil
	records.Origins[1].IndexedDBStatus = protocol.SurfaceStatus{State: protocol.Incomplete}
	snap := storageSnapshot(t, 2, records)
	snap.Surfaces["indexed_db"] = "incomplete"
	if _, err = engine.Accept(context.Background(), "source", snap); err != nil {
		t.Fatal(err)
	}
	for key := range runner.values {
		if strings.HasPrefix(key, "https://example.test:"+destination.Hash(str("db"))+":") {
			t.Fatal("revoked origin record survived", key)
		}
	}
	found := false
	for key := range runner.values {
		if strings.HasPrefix(key, "https://other.test:"+destination.Hash(str("db"))+":") {
			found = true
		}
	}
	if !found {
		t.Fatal("incomplete origin lost retained records")
	}
}
func TestDisjointSourcesShareAdditiveSchemaNotRecordOwnership(t *testing.T) {
	runner := &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}
	a := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, a)
	if err != nil {
		t.Fatal(err)
	}
	first := recordsFixture()
	first.Origins[0].LocalStorage = nil
	first.Origins[0].IndexedDB[0].Stores[0].Records = first.Origins[0].IndexedDB[0].Stores[0].Records[:1]
	status, err := engine.Accept(context.Background(), "first", storageSnapshot(t, 1, first))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	second := recordsFixture()
	second.Origins[0].LocalStorage = nil
	second.Origins[0].IndexedDB[0].Stores[0].Records = second.Origins[0].IndexedDB[0].Stores[0].Records[1:]
	status, err = engine.Accept(context.Background(), "second", storageSnapshot(t, 1, second))
	if err != nil || !status.Ready || status.Conflicts != 0 {
		t.Fatal(status, err)
	}
	// A value-only update must resolve the unchanged shared schema sentinel even
	// though no schema write is included in the current operation batch.
	second.Origins[0].IndexedDB[0].Stores[0].Records[0].Value = str("changed")
	status, err = engine.Accept(context.Background(), "second", storageSnapshot(t, 2, second))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if len(runner.values) != 2 {
		t.Fatal("disjoint source record lost", runner.values)
	}
	status, err = engine.Accept(context.Background(), "third", storageSnapshot(t, 1, first))
	if err != nil || status.Conflicts != 1 {
		t.Fatal("true record collision not preserved", status, err)
	}
}

type batchMemoryRunner struct {
	*memoryRunner
	reads         int
	failWrite     bool
	failRead      bool
	schemaMissing bool
}

func (*batchMemoryRunner) BatchReads() bool { return true }
func (r *batchMemoryRunner) Run(ctx context.Context, c cdp.BrowserContextID, origin, op string, args map[string]any, out any) error {
	if op != "readIndexedDBBatch" {
		if op == "indexedDB" && r.schemaMissing {
			if args["deletes_only"] != true || len(args["database"].(*protocol.IDBDatabase).Stores) != 0 {
				return errors.New("fixture schema conflict")
			}
		}
		if op == "indexedDB" && r.failWrite {
			return errors.New("fixture transaction aborted")
		}
		return r.memoryRunner.Run(ctx, c, origin, op, args, out)
	}
	r.reads++
	if r.failRead {
		return errors.New("fixture read unavailable")
	}
	db := args["database"].(*protocol.IDBDatabase)
	stores := args["stores"].([]readbackStore)
	result := make([][]map[string]any, len(stores))
	for n, store := range stores {
		result[n] = make([]map[string]any, len(store.Keys))
		for k, key := range store.Keys {
			value, ok := r.values[origin+":"+destination.Hash(db.Name)+":"+destination.Hash(store.Name)+":"+destination.Hash(key)]
			result[n][k] = map[string]any{"absent": !ok}
			if ok {
				result[n][k]["value"] = value
			}
		}
	}
	_, schema := r.schemas[destination.Hash(db.Name)]
	return json.Unmarshal(raw(map[string]any{"schema": schema && !r.schemaMissing, "stores": result}), out)
}
func TestDatabaseReadbackBatchesValuesBeforeAndAfterMutation(t *testing.T) {
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	store := &records.Origins[0].IndexedDB[0].Stores[0]
	store.Records = nil
	for n := range 500 {
		store.Records = append(store.Records, protocol.IDBRecord{Key: str(fmt.Sprint(n)), Value: str("before")})
	}
	status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if runner.reads != 2 {
		t.Fatalf("500 records need two database readbacks, got %d", runner.reads)
	}
	store.Records[17].Value = str("after")
	status, err = engine.Accept(context.Background(), "source", storageSnapshot(t, 2, records))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if runner.reads != 4 {
		t.Fatalf("mutation did not invalidate readback snapshot: %d", runner.reads)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.readback != nil {
		t.Fatal("reconcile retained database readback")
	}
}
func TestFailedDatabaseWriteCannotReusePrewriteReadback(t *testing.T) {
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records))
	if err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	records.Origins[0].IndexedDB[0].Stores[0].Records[0].Value = str("not-committed")
	runner.failWrite = true
	status, err = engine.Accept(context.Background(), "source", storageSnapshot(t, 2, records))
	if err != nil || status.Ready || status.Pending["indexed_db"] == 0 {
		t.Fatal("aborted batch claimed success", status, err)
	}
	if runner.reads != 4 {
		t.Fatal("aborted write did not invalidate prior snapshot", runner.reads)
	}
	for _, value := range runner.values {
		if string(value) == string(str("not-committed")) {
			t.Fatal("aborted transaction changed value")
		}
	}
}
func TestFailedDatabaseReadbackIsNotRetriedForEveryRecord(t *testing.T) {
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}, failRead: true}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, recordsFixture()))
	if err != nil || status.Ready {
		t.Fatal(status, err)
	}
	if runner.reads != 1 {
		t.Fatalf("failed group retried %d RPCs", runner.reads)
	}
}
func TestEmptyDatabaseSchemaSerializesArraysAndRecoverySkipsMetadata(t *testing.T) {
	records := recordsFixture()
	records.Origins[0].LocalStorage = nil
	records.Origins[0].IndexedDB[0].Stores = []protocol.IDBStore{}
	snap := storageSnapshot(t, 1, records)
	if len(snap.Items) != 1 {
		t.Fatal(snap.Items)
	}
	p, err := payload(snap.Items[0])
	if err != nil || p.Database.Stores == nil {
		t.Fatal("empty database became null", p, err)
	}
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	if err := adapter.PrepareBatch(context.Background(), destination.Context{}, snap.Items); err != nil {
		t.Fatal(err)
	}
	if adapter.readback.Stores == nil || adapter.readback.Database.Stores == nil {
		t.Fatal("empty serialized plan contains null arrays")
	}
	if err := adapter.PrepareRecovery(context.Background(), destination.Context{}, snap.Items); err != nil {
		t.Fatal(err)
	}
	if adapter.readback != nil {
		t.Fatal("schema generations were batched as one metadata identity")
	}
}
func TestPoisonedRecordDoesNotBlockUnrelatedOwnedRevocation(t *testing.T) {
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	if status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	prefix := "https://example.test:" + destination.Hash(str("db")) + ":" + destination.Hash(str("tokens")) + ":"
	runner.values[prefix+destination.Hash(str("b"))] = json.RawMessage(`{"t":"unsupported-local-value"}`)
	records.Origins[0].IndexedDB[0].Stores[0].Records = records.Origins[0].IndexedDB[0].Stores[0].Records[1:]
	if _, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 2, records)); err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.values[prefix+destination.Hash(str("a"))]; ok {
		t.Fatal("poisoned b blocked owned a revocation")
	}
}
func TestActiveIncompatibleSchemaDoesNotBlockDeletionOnlyRecords(t *testing.T) {
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: fixtureBase{}, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	records.Origins[0].LocalStorage = nil
	if status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 1, records)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	records.Origins[0].IndexedDB[0].Stores[0].Records = nil
	records.Origins[0].IndexedDB[0].Stores = append(records.Origins[0].IndexedDB[0].Stores, protocol.IDBStore{Name: str("missing-store"), KeyPath: json.RawMessage("null")})
	runner.schemaMissing = true
	status, err := engine.Accept(context.Background(), "source", storageSnapshot(t, 2, records))
	if err != nil || status.Ready || status.Pending["indexed_db"] == 0 {
		t.Fatal("schema conflict must remain pending", status, err)
	}
	if len(runner.values) != 0 {
		t.Fatal("active incompatible schema blocked owned logout", runner.values)
	}
}

type restartStorageBase struct {
	fixtureBase
	current destination.Context
}

func (b *restartStorageBase) Contexts(context.Context) ([]destination.Context, error) {
	return []destination.Context{b.current}, nil
}
func TestStorageOwnershipSurvivesOwnedRestartButNotRevokedOwnerHandoff(t *testing.T) {
	base := &restartStorageBase{current: destination.Context{ID: "default", Incarnation: "one", Default: true, OwnedProfile: "verified-fixture-profile"}}
	runner := &batchMemoryRunner{memoryRunner: &memoryRunner{values: map[string]json.RawMessage{}, schemas: map[string]protocol.IDBDatabase{}}}
	adapter := &Adapter{Base: base, Runner: runner}
	engine, err := destination.Open(t.TempDir(), nil, adapter)
	if err != nil {
		t.Fatal(err)
	}
	records := recordsFixture()
	ctx := context.Background()
	if status, err := engine.Accept(ctx, "alice", storageSnapshot(t, 1, records)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	// The browser's owned default profile persists LS/IDB through process restart.
	base.current.Incarnation = "two"
	empty := &protocol.StorageRecords{Version: 1}
	if status, err := engine.Accept(ctx, "alice", storageSnapshot(t, 2, empty)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if len(runner.values) != 0 {
		t.Fatal("owned default restart lost logout ownership", runner.values)
	}
	if status, err := engine.Accept(ctx, "bob", storageSnapshot(t, 1, records)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if status, err := engine.Accept(ctx, "alice", storageSnapshot(t, 3, empty)); err != nil || !status.Ready {
		t.Fatal(status, err)
	}
	if len(runner.values) != 3 {
		t.Fatal("revoked owner's old intents deleted new owner's storage", runner.values)
	}
}
