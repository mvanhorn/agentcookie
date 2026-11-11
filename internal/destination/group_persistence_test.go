package destination

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type durableBatchMemory struct {
	recoveryCalls   int
	recoveryItems   int
	requireRecovery bool
	prepared        bool
	*memoryApplier
	engine      *Engine
	dir         string
	writes      int
	readFailure string
	afterWrite  bool
	expected    int
}

func (m *durableBatchMemory) BatchKey(Item) string { return "fixture-database" }
func (m *durableBatchMemory) ApplyBatch(ctx context.Context, c Context, items []Item) error {
	// A real browser transaction may begin only after every record's intent is
	// readable in the durable journal; the sync-boundary counter is checked too.
	log, err := m.engine.readLog(filepath.Join(m.dir, "source", "intents.log"))
	if err != nil {
		return err
	}
	pending := map[string]bool{}
	for _, in := range log {
		pending[in.ID] = !in.Done
	}
	count := 0
	for _, v := range pending {
		if v {
			count++
		}
	}
	if count < m.expected {
		return fmt.Errorf("batch began with only %d/%d intents", count, m.expected)
	}
	for _, i := range items {
		if i.Deleted {
			m.Delete(ctx, c, i)
		} else {
			m.Set(ctx, c, i)
		}
	}
	m.writes++
	m.afterWrite = true
	return nil
}
func (m *durableBatchMemory) PrepareRecovery(_ context.Context, _ Context, items []Item) error {
	m.recoveryCalls++
	m.recoveryItems += len(items)
	m.prepared = true
	return nil
}
func (m *durableBatchMemory) Read(ctx context.Context, c Context, i Item) (string, bool, error) {
	if m.requireRecovery && !m.prepared {
		return "", false, errors.New("recovery was not prepared as a group")
	}
	if m.afterWrite && i.Key == m.readFailure {
		return "", false, errors.New("fixture readback unavailable")
	}
	return m.memoryApplier.Read(ctx, c, i)
}
func groupRecords(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = item(fmt.Sprintf("key-%05d", i), "value")
	}
	return out
}
func newDurableBatch(t *testing.T, n int) (*Engine, *durableBatchMemory) {
	t.Helper()
	dir := t.TempDir()
	m := &durableBatchMemory{memoryApplier: &memoryApplier{values: map[string]string{}}, dir: dir, expected: n}
	e, err := Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	return e, m
}
func TestGroupPersistenceUsesConstantDurableBoundaries(t *testing.T) {
	const count = 500
	e, m := newDurableBatch(t, count)
	boundaries := map[string]int{}
	e.Fault = func(stage string) error { boundaries[stage]++; return nil }
	started := time.Now()
	st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(count)...))
	t.Logf("%d fixture records reconciled in %s (not a browser benchmark)", count, time.Since(started))
	if err != nil || !st.Ready {
		t.Fatalf("batch: %+v %v", st, err)
	}
	if m.writes != 1 {
		t.Fatalf("browser writes %d", m.writes)
	}
	if boundaries["after-intent-log-sync"] != 2 || boundaries["before-ledger-save"] > 2 {
		t.Fatalf("per-record persistence remains: %v", boundaries)
	}
}
func TestGroupPersistenceCrashBoundariesRecoverEveryRecord(t *testing.T) {
	for _, stage := range []string{"after-intent", "after-write", "after-group-ledger", "after-group-completion"} {
		t.Run(stage, func(t *testing.T) {
			e, m := newDurableBatch(t, 12)
			e.Fault = func(at string) error {
				if at == stage {
					return errors.New("injected group crash")
				}
				return nil
			}
			if _, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...)); err == nil {
				t.Fatal("crash point did not execute")
			}
			e, err := Open(m.dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			m.engine = e
			st, err := e.Reconcile(context.Background(), "source")
			if err != nil || !st.Ready || len(m.values) != 12 {
				t.Fatalf("recover: %+v values=%d %v", st, len(m.values), err)
			}
			st, err = e.Accept(context.Background(), "source", snap(2))
			if err != nil || !st.Ready || len(m.values) != 0 {
				t.Fatalf("logout: %+v values=%d %v", st, len(m.values), err)
			}
		})
	}
}
func TestGroupReadbackFailureRetainsOnlyVerifiedOwnership(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	m.readFailure = "key-00003"
	st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
	if err != nil || st.Ready || len(st.Pending) == 0 {
		t.Fatalf("partial verification: %+v %v", st, err)
	}
	for _, l := range e.sources["source"].Ledgers {
		if len(l.Items) != 11 {
			t.Fatalf("owned %d records before final verification", len(l.Items))
		}
	}
	m.readFailure = ""
	e, err = Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	st, err = e.Reconcile(context.Background(), "source")
	if err != nil || !st.Ready || m.writes != 1 {
		t.Fatalf("readback recovery rewrote batch: %+v writes=%d %v", st, m.writes, err)
	}
}

func TestGroupLedgerFailuresNeverPublishUnsavedOwnershipOrReadiness(t *testing.T) {
	for _, failSave := range []int{1, 2} {
		t.Run(fmt.Sprintf("save-%d", failSave), func(t *testing.T) {
			e, m := newDurableBatch(t, 12)
			saves := 0
			e.Fault = func(stage string) error {
				if stage == "before-ledger-save" {
					saves++
					if saves == failSave {
						return errors.New("fixture ledger disk failure")
					}
				}
				return nil
			}
			st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
			if err == nil || st.Ready {
				t.Fatalf("failed ledger save reported ready: %+v %v", st, err)
			}
			st, _ = e.Status("source")
			if st.Ready || st.Applied["cookies"] != 0 {
				t.Fatalf("unsaved Applied escaped through status: %+v", st)
			}
			if failSave == 1 {
				for _, l := range e.sources["source"].Ledgers {
					if len(l.Items) != 0 {
						t.Fatal("failed ownership checkpoint published")
					}
				}
			}
			reopened, openErr := Open(m.dir, nil, m)
			if openErr != nil {
				t.Fatal(openErr)
			}
			for _, l := range reopened.sources["source"].Ledgers {
				if l.Applied["cookies"] != 0 {
					t.Fatal("failed Applied checkpoint appeared after reopen")
				}
			}
			// The original in-memory engine must retry correctly as well as a reopen.
			e.Fault = nil
			st, err = e.Reconcile(context.Background(), "source")
			if err != nil || !st.Ready || m.writes != 1 {
				t.Fatalf("memory retry: %+v writes=%d %v", st, m.writes, err)
			}
			e, err = Open(m.dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			m.engine = e
			st, err = e.Reconcile(context.Background(), "source")
			if err != nil || !st.Ready || m.writes != 1 {
				t.Fatalf("reopen: %+v writes=%d %v", st, m.writes, err)
			}
		})
	}
}

func TestUncertainGroupAppendBlocksFurtherWritesUntilReload(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	path := filepath.Join(m.dir, "source", "intents.log")
	e.Fault = func(stage string) error {
		if stage != "before-intent-log-sync" {
			return nil
		}
		// Simulate a torn trailing record after a partial/uncertain append. The
		// in-memory engine must never append another group behind this suffix.
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString("{")
		f.Close()
		if err != nil {
			return err
		}
		return errors.New("fixture uncertain journal write")
	}
	st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
	if err == nil || st.Ready || m.writes != 0 || len(e.sources["source"].Intents) != 0 {
		t.Fatalf("uncertain append published or wrote browser: %+v %v", st, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e.Fault = nil
	st, err = e.Reconcile(context.Background(), "source")
	if err == nil || st.Ready || m.writes != 0 {
		t.Fatal("uncertain journal did not fail closed")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("appended behind corrupt suffix")
	}
	e, err = Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	st, err = e.Reconcile(context.Background(), "source")
	if err != nil || st.RepairNeeded == nil || st.Ready || len(m.values) != 12 {
		t.Fatalf("reload failed to preserve repair reporting: %+v %v", st, err)
	}
	st, err = e.Repair(context.Background(), "source", true)
	if err != nil || !st.Ready {
		t.Fatalf("explicit repair: %+v %v", st, err)
	}
}

func TestSupersededGroupRecoveryPreparesReadsAndCheckpointsOnce(t *testing.T) {
	const count = 500
	e, m := newDurableBatch(t, count)
	e.Fault = func(stage string) error {
		if stage == "after-write" {
			return errors.New("fixture post-write crash")
		}
		return nil
	}
	if _, err := e.Accept(context.Background(), "source", snap(1, groupRecords(count)...)); err == nil {
		t.Fatal("crash not injected")
	}
	e, err := Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	m.requireRecovery = true
	boundaries := map[string]int{}
	e.Fault = func(stage string) error { boundaries[stage]++; return nil }
	started := time.Now()
	st, err := e.Accept(context.Background(), "source", snap(2))
	t.Logf("%d superseded fixture intents recovered/revoked in %s (not a browser benchmark)", count, time.Since(started))
	if err != nil || !st.Ready || len(m.values) != 0 {
		t.Fatalf("superseded logout: %+v values=%d %v", st, len(m.values), err)
	}
	if m.recoveryCalls != 1 || m.recoveryItems != count {
		t.Fatalf("recovery reads not grouped: calls=%d items=%d", m.recoveryCalls, m.recoveryItems)
	}
	if boundaries["after-intent-log-sync"] != 3 || boundaries["before-ledger-save"] > 3 {
		t.Fatalf("recovery persisted per record: %v", boundaries)
	}
}

func TestRecoveryCheckpointFailureDoesNotPublishOwnership(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	e.Fault = func(stage string) error {
		if stage == "after-write" {
			return errors.New("fixture crash")
		}
		return nil
	}
	e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
	e, err := Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	e.Fault = func(stage string) error {
		if stage == "before-ledger-save" {
			return errors.New("fixture recovery checkpoint failure")
		}
		return nil
	}
	st, err := e.Accept(context.Background(), "source", snap(2))
	if err == nil || st.Ready {
		t.Fatal("failed recovery checkpoint reported ready")
	}
	for _, l := range e.sources["source"].Ledgers {
		if len(l.Items) != 0 {
			t.Fatal("recovery published ownership before checkpoint")
		}
	}
	for _, in := range e.sources["source"].Intents {
		if in.Done {
			t.Fatal("recovery abandoned intent before checkpoint")
		}
	}
	e.Fault = nil
	st, err = e.Reconcile(context.Background(), "source")
	if err != nil || !st.Ready || len(m.values) != 0 {
		t.Fatalf("recovery retry failed: %+v values=%d %v", st, len(m.values), err)
	}
}

func TestOwnedDefaultCarryCheckpointFailureDoesNotPublishNewLedger(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	first := Context{ID: "default", Incarnation: "one", Default: true, OwnedProfile: "fixture-owned-profile"}
	next := first
	next.Incarnation = "two"
	m.contexts = []Context{first}
	records := groupRecords(12)
	if st, err := e.Accept(context.Background(), "source", snap(1, records...)); err != nil || !st.Ready {
		t.Fatalf("setup: %+v %v", st, err)
	}
	for _, i := range records {
		m.values[next.Key()+i.ID()] = m.values[first.Key()+i.ID()]
		delete(m.values, first.Key()+i.ID())
	}
	m.contexts = []Context{next}
	e.Fault = func(stage string) error {
		if stage == "before-ledger-save" {
			return errors.New("fixture carry checkpoint failure")
		}
		return nil
	}
	if st, err := e.Accept(context.Background(), "source", snap(2)); err == nil || st.Ready {
		t.Fatalf("failed carry: %+v %v", st, err)
	}
	state := e.sources["source"]
	if state.Ledgers[next.Key()] != nil || state.Desired.OwnedDefaults[first.OwnedProfile] != first.Key() {
		t.Fatal("failed carry published context ownership")
	}
	e.Fault = nil
	st, err := e.Reconcile(context.Background(), "source")
	if err != nil || !st.Ready || len(m.values) != 0 {
		t.Fatalf("carry retry: %+v values=%d %v", st, len(m.values), err)
	}
}

func TestCompletionAppendFailureKeepsPendingUntilReload(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	syncs := 0
	e.Fault = func(stage string) error {
		if stage == "before-intent-log-sync" {
			syncs++
			if syncs == 2 {
				return errors.New("fixture completion sync uncertainty")
			}
		}
		return nil
	}
	st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
	if err == nil || st.Ready || m.writes != 1 {
		t.Fatalf("completion uncertainty: %+v writes=%d %v", st, m.writes, err)
	}
	for _, in := range e.sources["source"].Intents {
		if in.Done {
			t.Fatal("uncertain completion published in memory")
		}
	}
	if st, _ = e.Status("source"); st.Ready {
		t.Fatal("uncertain completion status ready")
	}
	e, err = Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	st, err = e.Reconcile(context.Background(), "source")
	if err != nil || !st.Ready || m.writes != 1 {
		t.Fatalf("completion reload rewrote browser: %+v writes=%d %v", st, m.writes, err)
	}
}

func TestSupersededLogoutIntentSurvivesCheckpointLoss(t *testing.T) {
	for _, logoutCrash := range []string{"after-intent", "after-write"} {
		t.Run(logoutCrash, func(t *testing.T) {
			e, m := newDurableBatch(t, 12)
			e.Fault = func(stage string) error {
				if stage == "after-write" {
					return errors.New("set crash")
				}
				return nil
			}
			e.Accept(context.Background(), "source", snap(1, groupRecords(12)...))
			e, err := Open(m.dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			m.engine = e
			e.Fault = func(stage string) error {
				if stage == logoutCrash {
					return errors.New("logout crash")
				}
				return nil
			}
			if _, err = e.Accept(context.Background(), "source", snap(2)); err == nil {
				t.Fatal("logout crash not injected")
			}
			entries, err := os.ReadDir(filepath.Join(m.dir, "source", "applied"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if err = os.Remove(filepath.Join(m.dir, "source", "applied", entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
			e, err = Open(m.dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			m.engine = e
			st, err := e.Reconcile(context.Background(), "source")
			if err != nil || !st.Ready || len(m.values) != 0 {
				t.Fatalf("surviving logout intent did not recover lost checkpoint: %+v values=%d %v", st, len(m.values), err)
			}
		})
	}
}

func TestUncertainDeleteCompletionCannotReleaseOwnershipEarly(t *testing.T) {
	e, m := newDurableBatch(t, 12)
	if st, err := e.Accept(context.Background(), "source", snap(1, groupRecords(12)...)); err != nil || !st.Ready {
		t.Fatalf("setup: %+v %v", st, err)
	}
	syncs := 0
	e.Fault = func(stage string) error {
		if stage == "before-intent-log-sync" {
			syncs++
			if syncs == 2 {
				return errors.New("delete completion sync uncertainty")
			}
		}
		return nil
	}
	if st, err := e.Accept(context.Background(), "source", snap(2)); err == nil || st.Ready {
		t.Fatalf("delete completion: %+v %v", st, err)
	}
	e.Fault = nil
	st, err := e.Accept(context.Background(), "other", snap(1, groupRecords(12)...))
	if err != nil || st.Conflicts != 12 || st.Ready {
		t.Fatalf("new owner acquired before deletion completion was durable: %+v %v", st, err)
	}
	e, err = Open(m.dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	m.engine = e
	if st, err = e.Reconcile(context.Background(), "source"); err != nil || !st.Ready {
		t.Fatalf("delete completion recovery: %+v %v", st, err)
	}
	// Disable the fixture's source-specific journal assertion for this new owner.
	plain := m.memoryApplier
	e.applier = plain
	st, err = e.Accept(context.Background(), "other", snap(2, groupRecords(12)...))
	if err != nil || !st.Ready || st.Conflicts != 0 {
		t.Fatalf("completed deletion retained ownership: %+v %v", st, err)
	}
	if _, err = e.Reconcile(context.Background(), "source"); err != nil || len(m.values) != 12 {
		t.Fatal("old tombstone overrode new owner")
	}
}
