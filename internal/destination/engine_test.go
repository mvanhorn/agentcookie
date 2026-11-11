package destination

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type memoryApplier struct {
	values   map[string]string
	failKey  string
	contexts []Context
}

func (m *memoryApplier) Contexts(context.Context) ([]Context, error) {
	if m.contexts == nil {
		return []Context{{ID: "default", Incarnation: "p1", Default: true}}, nil
	}
	return m.contexts, nil
}
func (m *memoryApplier) Read(_ context.Context, c Context, i Item) (string, bool, error) {
	v, ok := m.values[c.Key()+i.ID()]
	return v, ok, nil
}
func (m *memoryApplier) Set(_ context.Context, c Context, i Item) error {
	if i.Key == m.failKey {
		return errors.New("quota")
	}
	m.values[c.Key()+i.ID()] = i.Hash
	return nil
}
func (m *memoryApplier) Delete(_ context.Context, c Context, i Item) error {
	delete(m.values, c.Key()+i.ID())
	return nil
}
func item(key, value string) Item {
	return NewItem("cookies", "example.test", key, []byte(`"`+value+`"`))
}
func snap(g uint64, items ...Item) Snapshot {
	return Snapshot{Generation: g, Items: items, Surfaces: map[string]string{"cookies": "complete"}}
}
func TestOwnershipRotationLogoutAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, err := Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	a := item("token", "one")
	b := item("token", "two")
	if s, err := e.Accept(ctx, "source", snap(1, a)); err != nil || !s.Ready {
		t.Fatalf("first: %+v %v", s, err)
	}
	if s, err := e.Accept(ctx, "other", snap(1, b)); err != nil || s.Conflicts != 1 {
		t.Fatalf("conflict: %+v %v", s, err)
	}
	if _, err = e.Accept(ctx, "source", snap(2, b)); err != nil {
		t.Fatal(err)
	}
	c := Context{ID: "default", Incarnation: "p1", Default: true}
	if m.values[c.Key()+b.ID()] != b.Hash {
		t.Fatal("same key new value was not applied")
	}
	sinkOnly := item("local", "keep")
	m.values[c.Key()+sinkOnly.ID()] = sinkOnly.Hash
	e, err = Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Accept(ctx, "source", snap(3)); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.values[c.Key()+a.ID()]; ok {
		t.Fatal("revoked cookie survived")
	}
	if m.values[c.Key()+sinkOnly.ID()] != sinkOnly.Hash {
		t.Fatal("sink-only cookie deleted")
	}
	if s, err := e.Accept(ctx, "other", snap(2, b)); err != nil || !s.Ready {
		t.Fatalf("released ownership: %+v %v", s, err)
	}
	if _, err = e.Reconcile(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if m.values[c.Key()+b.ID()] != b.Hash {
		t.Fatal("old owner's tombstone deleted the new owner's record")
	}
}
func TestCrashRecoveryValueVerification(t *testing.T) {
	for _, browserWriteSurvives := range []bool{false, true} {
		t.Run(map[bool]string{true: "after-write", false: "before-write"}[browserWriteSurvives], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			m := &memoryApplier{values: map[string]string{}}
			e, _ := Open(dir, nil, m)
			a := item("token", "one")
			e.Fault = func(stage string) error {
				if stage == "after-intent" && !browserWriteSurvives || stage == "after-write" && browserWriteSurvives {
					return errors.New("crash")
				}
				return nil
			}
			if _, err := e.Accept(ctx, "source", snap(1, a)); err == nil {
				t.Fatal("missing injected crash")
			}
			e, err := Open(dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			s, err := e.Reconcile(ctx, "source")
			if err != nil || !s.Ready {
				t.Fatalf("recover: %+v %v", s, err)
			}
			if h, ok, _ := m.Read(ctx, Context{ID: "default", Incarnation: "p1"}, a); !ok || h != a.Hash {
				t.Fatal("not restored")
			}
		})
	}
}
func TestIncompleteHeldAndNewContext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(dir, nil, m)
	a := item("token", "one")
	e.Accept(ctx, "source", snap(1, a))
	s := snap(2)
	s.Surfaces["cookies"] = "incomplete"
	e.Accept(ctx, "source", s)
	e, _ = Open(dir, nil, m)
	m.contexts = []Context{{ID: "default", Incarnation: "p2", Default: true}, {ID: "private", Incarnation: "p2"}}
	if _, err := e.Reconcile(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if len(m.values) != 3 {
		t.Fatalf("new contexts not fully restored: %v", m.values)
	}
	s = snap(3)
	s.Sites = map[string]string{"example.test": "held"}
	e.Accept(ctx, "source", s)
	if len(m.values) != 3 {
		t.Fatal("held removed")
	}
}
func TestLostLedgerAndIntentsNeedsRepair(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(dir, nil, m)
	e.Accept(ctx, "source", snap(4, item("token", "one")))
	os.RemoveAll(filepath.Join(dir, "source", "applied"))
	os.Remove(filepath.Join(dir, "source", "intents.log"))
	e, err := Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Reconcile(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	if s.RepairNeeded == nil || s.Ready || s.RepairNeeded.Generation != 4 {
		t.Fatalf("not repair-needed: %+v", s)
	}
	if _, err := e.Repair(ctx, "source", false); err == nil {
		t.Fatal("repair did not require operator confirmation")
	}
	s, err = e.Repair(ctx, "source", true)
	if err != nil || !s.Ready {
		t.Fatalf("repair: %+v %v", s, err)
	}
}
func TestPartialFailureAndStaleGeneration(t *testing.T) {
	ctx := context.Background()
	m := &memoryApplier{values: map[string]string{}, failKey: "b"}
	e, _ := Open(t.TempDir(), nil, m)
	s, _ := e.Accept(ctx, "source", snap(1, item("a", "ok"), item("b", "no")))
	if len(m.values) != 1 || s.Ready || len(s.Failed) == 0 {
		t.Fatalf("partial %+v", s)
	}
	m.failKey = ""
	s, _ = e.Accept(ctx, "source", snap(2))
	if !s.Ready || len(m.values) != 0 {
		t.Fatalf("logout %+v", s)
	}
	if _, err := e.Accept(ctx, "source", snap(1, item("a", "old"))); err == nil {
		t.Fatal("stale generation accepted")
	}
	if len(m.values) != 0 {
		t.Fatal("stale resurrected")
	}
}

func TestIncompleteCookiesApplyWithoutRevoking(t *testing.T) {
	ctx := context.Background()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(t.TempDir(), nil, m)
	e.Accept(ctx, "source", snap(1, item("keep", "one")))
	s := snap(2, item("new", "two"))
	s.Surfaces["cookies"] = "incomplete"
	st, err := e.Accept(ctx, "source", s)
	if err != nil || !st.Ready || len(m.values) != 2 {
		t.Fatalf("incomplete additive: %+v %v values=%v", st, err, m.values)
	}
}
func TestStorageSiteMatching(t *testing.T) {
	s := Snapshot{Sites: map[string]string{"example.test": "held"}}
	if siteState(s, Item{Scope: "https://app.example.test:8123/.agentcookie-db/eA"}) != "held" {
		t.Fatal("origin host not normalized")
	}
}
func TestCurrentIncarnationControlsReady(t *testing.T) {
	ctx := context.Background()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(t.TempDir(), nil, m)
	e.Accept(ctx, "source", snap(1, item("a", "one")))
	m.contexts = []Context{{ID: "default", Incarnation: "p2", Default: true}}
	m.failKey = "a"
	st, err := e.Reconcile(ctx, "source")
	if err != nil || st.Ready || st.Applied["cookies"] != 0 {
		t.Fatalf("old process certified new context: %+v %v", st, err)
	}
}
func TestDesiredWriteFailureAndInvalidSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(dir, nil, m)
	e.Accept(ctx, "source", snap(1, item("a", "one")))
	bad := snap(2, item("b", "two"))
	bad.Surfaces["cookies"] = "oops"
	if _, err := e.Accept(ctx, "source", bad); err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	if st, _ := e.Status("source"); st.Accepted != 1 {
		t.Fatal("invalid snapshot persisted")
	}
	e.Fault = func(stage string) error {
		if stage == "before-desired-save" {
			return errors.New("disk failure")
		}
		return nil
	}
	if _, err := e.Accept(ctx, "source", snap(2, item("a", "two"))); err == nil {
		t.Fatal("fault not injected")
	}
	if e.sources["source"].Desired.Generation != 1 {
		t.Fatal("non-durable desired advanced")
	}
	e.Fault = nil
	e, _ = Open(dir, nil, m)
	if st, err := e.Reconcile(ctx, "source"); err != nil || !st.Ready || st.Accepted != 2 {
		t.Fatalf("accepted recovery: %+v %v", st, err)
	}
}

func TestOneMissingContextLedgerNeedsRepair(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}, contexts: []Context{{ID: "default", Incarnation: "p1", Default: true}, {ID: "private", Incarnation: "p1"}}}
	e, _ := Open(dir, nil, m)
	e.Accept(ctx, "source", snap(7, item("a", "one")))
	os.Remove(filepath.Join(dir, "source", "applied", m.contexts[0].Key()+".json"))
	os.Remove(filepath.Join(dir, "source", "intents.log"))
	e, err := Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := e.Reconcile(ctx, "source")
	if st.RepairNeeded == nil || st.RepairNeeded.Generation != 7 {
		t.Fatalf("missing one context history not reported: %+v", st)
	}
}
func TestRepeatedReconcileDoesNotGrowIntentLog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(dir, nil, m)
	e.Accept(ctx, "source", snap(1, item("a", "one")))
	before, _ := os.Stat(filepath.Join(dir, "source", "intents.log"))
	for range 5 {
		e.Reconcile(ctx, "source")
	}
	after, _ := os.Stat(filepath.Join(dir, "source", "intents.log"))
	if before.Size() != after.Size() {
		t.Fatal("unchanged verified data appended intents")
	}
}
func TestRevokedOwnerReleasedAfterProcessRestart(t *testing.T) {
	ctx := context.Background()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(t.TempDir(), nil, m)
	e.Accept(ctx, "a", snap(1, item("token", "one")))
	m.contexts = []Context{{ID: "default", Incarnation: "p2", Default: true}}
	e.Accept(ctx, "a", snap(2))
	st, err := e.Accept(ctx, "b", snap(1, item("token", "two")))
	if err != nil || st.Conflicts != 0 || !st.Ready {
		t.Fatalf("dead context retained ownership: %+v %v", st, err)
	}
}

func TestRepeatedFailedIntentCompactsByLogEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}, failKey: "a"}
	e, _ := Open(dir, nil, m)
	e.Accept(ctx, "source", snap(1, item("a", "one")))
	s := e.sources["source"]
	var pending intent
	for _, in := range s.Intents {
		pending = in
	}
	for range 1030 {
		if err := e.appendIntent("source", s, pending); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.compact("source", s); err != nil {
		t.Fatal(err)
	}
	if s.LogEntries != 1 {
		t.Fatalf("same-ID failed attempts not compacted: %d", s.LogEntries)
	}
}

func TestSinkPolicyRetainsCookieHostKeyMatching(t *testing.T) {
	ctx := context.Background()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(t.TempDir(), nil, m)
	i := NewItem("cookies", ".example.test", "session", []byte(`"one"`))
	e.Accept(ctx, "source", snap(1, i))
	next := snap(2)
	next.Surfaces["cookies"] = "incomplete"
	e.FilterSnapshot(&next, func(host string) bool { return host != ".example.test" })
	st, err := e.Accept(ctx, "source", next)
	if err != nil || !st.Ready || len(m.values) != 0 {
		t.Fatalf("policy did not revoke dotted host during incomplete read: %+v %v", st, err)
	}
}

func TestOwnedPersistentDefaultLogoutAcrossBrowserRestart(t *testing.T) {
	for _, crashStage := range []string{"", "after-write", "after-intent"} {
		t.Run("crash-"+crashStage, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			first := Context{ID: "default", Incarnation: "p1", Default: true, OwnedProfile: "verified-dedicated-profile"}
			m := &memoryApplier{values: map[string]string{}, contexts: []Context{first}}
			e, _ := Open(dir, nil, m)
			if crashStage != "" {
				e.Fault = func(stage string) error {
					if stage == crashStage {
						return errors.New("injected crash")
					}
					return nil
				}
			}
			cookie := item("token", "one")
			e.Accept(ctx, "a", snap(1, cookie))
			m.contexts = []Context{}
			e.Fault = nil
			e.Accept(ctx, "a", snap(2)) // Durable logout accepted while Chrome is closed.
			next := first
			next.Incarnation = "p2"
			if hash, ok := m.values[first.Key()+cookie.ID()]; ok {
				m.values[next.Key()+cookie.ID()] = hash
			}
			m.contexts = []Context{next}
			// Include sink restart, not merely a second reconciliation in memory.
			e, err := Open(dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			st, err := e.Reconcile(ctx, "a")
			if err != nil || !st.Ready {
				t.Fatalf("restore: %+v %v", st, err)
			}
			if _, ok := m.values[next.Key()+cookie.ID()]; ok {
				t.Fatal("persistent owned cookie survived logout across browser restart")
			}
			other := item("token", "two")
			if st, err = e.Accept(ctx, "b", snap(1, other)); err != nil || !st.Ready {
				t.Fatalf("ownership not released: %+v %v", st, err)
			}
			e.Reconcile(ctx, "a")
			if m.values[next.Key()+cookie.ID()] != other.Hash {
				t.Fatal("revoked source deleted new owner after restart")
			}
		})
	}
}

func TestOwnershipNeverCrossesUnverifiedOrDifferentProfiles(t *testing.T) {
	for _, mode := range []string{"attach", "private", "different-profile"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			first := Context{ID: "default", Incarnation: "p1", Default: true, OwnedProfile: "owned-one"}
			next := first
			next.Incarnation = "p2"
			switch mode {
			case "attach":
				first.OwnedProfile = ""
				next.OwnedProfile = ""
			case "private":
				first.Default = false
				next.Default = false
			case "different-profile":
				next.OwnedProfile = "owned-two"
			}
			m := &memoryApplier{values: map[string]string{}, contexts: []Context{first}}
			e, _ := Open(t.TempDir(), nil, m)
			cookie := item("token", "one")
			e.Accept(ctx, "a", snap(1, cookie))
			m.contexts = []Context{}
			e.Accept(ctx, "a", snap(2))
			m.contexts = []Context{next}
			m.values[next.Key()+cookie.ID()] = cookie.Hash
			e.Reconcile(ctx, "a")
			if m.values[next.Key()+cookie.ID()] != cookie.Hash {
				t.Fatal("ownership crossed an unverified or different profile")
			}
		})
	}
}

func TestTornIntentTailCannotHideLaterCrashRecovery(t *testing.T) {
	for _, suffix := range []string{"{truncated", "complete-without-newline"} {
		t.Run(suffix, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			m := &memoryApplier{values: map[string]string{}}
			e, _ := Open(dir, nil, m)
			e.Accept(ctx, "a", snap(1, item("first", "one")))
			path := filepath.Join(dir, "a", "intents.log")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if suffix == "complete-without-newline" {
				body = body[:len(body)-1]
			} else {
				body = append(body, []byte(suffix)...)
			}
			if err = os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
			e, err = Open(dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.readLog(path); err != nil {
				t.Fatalf("corrupt suffix left in append path: %v", err)
			}
			e.Fault = func(stage string) error {
				if stage == "after-write" {
					return errors.New("second crash")
				}
				return nil
			}
			e.Accept(ctx, "a", snap(2, item("second", "two")))
			e, err = Open(dir, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			e.Accept(ctx, "a", snap(3))
			if len(m.values) != 0 {
				t.Fatalf("post-tail-repair crashed writes survived logout: %v", m.values)
			}
		})
	}
}

func TestPendingStatusTimingSurvivesGenerationsAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}, failKey: "token"}
	e, _ := Open(dir, nil, m)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	e.Now = func() time.Time { return now }
	first := now
	st, _ := e.Accept(ctx, "a", snap(1, item("token", "one")))
	if !st.PendingSince.Equal(first) || !st.LastAcceptedAt.Equal(first) || !st.LastAppliedAt.IsZero() {
		t.Fatalf("first pending timing: %+v", st)
	}
	now = now.Add(11 * time.Minute)
	st, _ = e.Accept(ctx, "a", snap(2, item("token", "two")))
	if !st.PendingSince.Equal(first) || !st.LastAcceptedAt.Equal(now) {
		t.Fatalf("new generation reset pending clock: %+v", st)
	}
	e, err := Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	e.Now = func() time.Time { return now }
	st, _ = e.Reconcile(ctx, "a")
	if !st.PendingSince.Equal(first) {
		t.Fatalf("restart reset pending clock: %+v", st)
	}
	m.failKey = ""
	now = now.Add(time.Minute)
	st, err = e.Reconcile(ctx, "a")
	applied := now
	if err != nil || !st.Ready || !st.PendingSince.IsZero() || !st.LastAppliedAt.Equal(applied) {
		t.Fatalf("ready timing: %+v %v", st, err)
	}
	now = now.Add(time.Minute)
	st, _ = e.Reconcile(ctx, "a")
	if !st.LastAppliedAt.Equal(applied) {
		t.Fatal("idle ready poll changed last application time")
	}
	m.contexts = []Context{}
	st, _ = e.Reconcile(ctx, "a")
	if !st.PendingSince.Equal(now) {
		t.Fatalf("outage did not start pending clock: %+v", st)
	}
	e, err = Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = e.Status("a")
	if !st.PendingSince.Equal(now) || !st.LastAppliedAt.Equal(applied) {
		t.Fatalf("outage timing not durable: %+v", st)
	}
}

type budgetApplier struct {
	*memoryApplier
	chosen string
}

func (m *budgetApplier) Read(ctx context.Context, c Context, i Item) (string, bool, error) {
	if m.chosen == "" {
		m.chosen = i.ID()
	}
	if i.ID() != m.chosen {
		return "", false, context.DeadlineExceeded
	}
	return m.memoryApplier.Read(ctx, c, i)
}
func TestFairGroupsMakeProgressUnderRepeatedShortBudgets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m := &budgetApplier{memoryApplier: &memoryApplier{values: map[string]string{}}}
	e, _ := Open(dir, nil, m)
	records := []Item{item("a", "one"), item("b", "one"), item("c", "one")}
	e.Accept(ctx, "a", snap(1, records...))
	for range 2 {
		m.chosen = ""
		var err error
		e, err = Open(dir, nil, m)
		if err != nil {
			t.Fatal(err)
		}
		e.Reconcile(ctx, "a")
	}
	if len(m.values) != 3 {
		t.Fatalf("never-applied groups starved across retries: %d", len(m.values))
	}
	for i := range records {
		records[i] = item(records[i].Key, "two")
	}
	m.chosen = ""
	e.Accept(ctx, "a", snap(2, records...))
	for range 2 {
		m.chosen = ""
		e.Reconcile(ctx, "a")
	}
	c := Context{ID: "default", Incarnation: "p1", Default: true}
	for _, i := range records {
		if m.values[c.Key()+i.ID()] != i.Hash {
			t.Fatalf("oldest applied group starved: %s", i.Key)
		}
	}
}

func TestCapExcludedPersistsInStatus(t *testing.T) {
	dir := t.TempDir()
	m := &memoryApplier{values: map[string]string{}}
	e, _ := Open(dir, nil, m)
	snapshot := snap(1, item("a", "one"))
	snapshot.CapExcluded = 17
	st, err := e.Accept(context.Background(), "a", snapshot)
	if err != nil || st.CapExcluded != 17 {
		t.Fatalf("reply lost exclusion count: %+v %v", st, err)
	}
	e, err = Open(dir, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	st, _ = e.Status("a")
	if st.CapExcluded != 17 {
		t.Fatalf("restart status lost exclusions: %+v", st)
	}
}
