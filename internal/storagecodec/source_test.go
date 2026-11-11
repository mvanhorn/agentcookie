package storagecodec

import (
	"strings"

	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/protocol"
)

type fakeCompanion struct {
	call  func(context.Context, map[string]any) (protocol.StorageOrigin, error)
	calls int
}

func (f *fakeCompanion) Call(ctx context.Context, method string, args, out any) error {
	f.calls++
	if method != "readStorage" {
		return errors.New("wrong method")
	}
	v, err := f.call(ctx, args.(map[string]any))
	if err != nil {
		return err
	}
	*out.(*protocol.StorageOrigin) = v
	return nil
}

type fakeBroker struct{ adds, removes int }

func (f *fakeBroker) AddReadIntercept(context.Context, string, string) error { f.adds++; return nil }
func (f *fakeBroker) RemoveReadIntercept(context.Context, string) error      { f.removes++; return nil }
func fixtureOrigin() protocol.StorageOrigin {
	return protocol.StorageOrigin{Origin: "https://example.test", Container: 2, LocalStorageStatus: protocol.SurfaceStatus{State: protocol.Complete}, IndexedDBStatus: protocol.SurfaceStatus{State: protocol.Complete}, LocalStorage: []protocol.LocalStorageItem{{Key: json.RawMessage(`{"t":"str","v":"token"}`), Value: json.RawMessage(`{"t":"str","v":"secret"}`)}}}
}
func TestSourceRetainsDataAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	s, err := NewSource("intercept", path)
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{}
	f := &fakeCompanion{call: func(_ context.Context, args map[string]any) (protocol.StorageOrigin, error) {
		if args["cookieStoreId"] != "firefox-container-2" {
			t.Fatal(args)
		}
		return fixtureOrigin(), nil
	}}
	requests := []OriginRequest{{Origin: "https://example.test", Container: 2, Due: true, Representable: true}}
	r, err := s.ReadCycle(context.Background(), f, b, requests, true)
	if err != nil || r.Surfaces[protocol.LocalStorage].State != protocol.Complete {
		t.Fatal(r, err)
	}
	if b.adds != 1 || b.removes != 1 {
		t.Fatal(b)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	s, err = NewSource("intercept", path)
	if err != nil {
		t.Fatal(err)
	}
	f.call = func(context.Context, map[string]any) (protocol.StorageOrigin, error) {
		return protocol.StorageOrigin{}, errors.New("timeout")
	}
	r, err = s.ReadCycle(context.Background(), f, b, requests, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Records.Origins[0].LocalStorage) != 1 || r.Surfaces[protocol.LocalStorage].State != protocol.Incomplete {
		t.Fatal(r)
	}
	if b.adds != 2 || b.removes != 2 {
		t.Fatal(b)
	}
}
func TestSourcePauseDoesNotSilentlyNavigate(t *testing.T) {
	s, _ := NewSource("intercept", "")
	f := &fakeCompanion{}
	_, err := s.ReadOrigin(context.Background(), f, nil, OriginRequest{Origin: "https://example.test"})
	if err == nil || f.calls != 0 {
		t.Fatal(err, f.calls)
	}
	s.Mode = "request"
	f.call = func(_ context.Context, args map[string]any) (protocol.StorageOrigin, error) {
		if args["mode"] != "request" {
			t.Fatal(args)
		}
		o := fixtureOrigin()
		o.Container = 0
		return o, nil
	}
	if _, err = s.ReadOrigin(context.Background(), f, nil, OriginRequest{Origin: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
}
func TestSourceCycleBudgetAndMissingCache(t *testing.T) {
	s, _ := NewSource("request", "")
	s.CycleBudget = time.Millisecond
	f := &fakeCompanion{call: func(ctx context.Context, args map[string]any) (protocol.StorageOrigin, error) {
		<-ctx.Done()
		return protocol.StorageOrigin{}, ctx.Err()
	}}
	r, err := s.ReadCycle(context.Background(), f, nil, []OriginRequest{{Origin: "https://a.test", Representable: true}, {Origin: "https://b.test", Representable: true}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 || len(r.Records.Origins) != 2 || r.Surfaces[protocol.IndexedDB].State != protocol.Incomplete {
		t.Fatal(r, f.calls)
	}
}
func TestSourceWrongIdentityRejectedAndInterceptRemoved(t *testing.T) {
	s, _ := NewSource("intercept", "")
	b := &fakeBroker{}
	f := &fakeCompanion{call: func(context.Context, map[string]any) (protocol.StorageOrigin, error) { return fixtureOrigin(), nil }}
	if _, err := s.ReadOrigin(context.Background(), f, b, OriginRequest{Origin: "https://wrong.test"}); err == nil {
		t.Fatal("identity accepted")
	}
	if b.removes != 1 {
		t.Fatal("intercept leaked")
	}
}
func TestSourceAggregateCapsQueueAndRotateOrigins(t *testing.T) {
	s, _ := NewSource("request", "")
	s.surfaceLimit = 250
	s.wireLimit = 2000
	f := &fakeCompanion{call: func(_ context.Context, args map[string]any) (protocol.StorageOrigin, error) {
		o := fixtureOrigin()
		o.Origin = args["origin"].(string)
		o.Container = 0
		o.LocalStorage[0].Value = str(strings.Repeat("x", 130))
		return o, nil
	}}
	requests := []OriginRequest{{Origin: "https://a.test", Representable: true}, {Origin: "https://b.test", Representable: true}}
	first, err := s.ReadCycle(context.Background(), f, nil, requests, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Surfaces[protocol.LocalStorage].State != protocol.Incomplete {
		t.Fatal("aggregate falsely complete")
	}
	complete := ""
	for _, o := range first.Records.Origins {
		if o.LocalStorageStatus.State == protocol.Complete {
			complete = o.Origin
		}
	}
	if complete == "" {
		t.Fatal("no origin progressed")
	}
	second, err := s.ReadCycle(context.Background(), f, nil, requests, true)
	if err != nil {
		t.Fatal(err)
	}
	advanced := false
	for _, o := range second.Records.Origins {
		if o.Origin != complete && o.LocalStorageStatus.State == protocol.Complete {
			advanced = true
		}
	}
	if !advanced {
		t.Fatal("queued origin starved")
	}
}
func TestSourceCacheEvictsUnroutedAndBoundsSparseFileBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	s, _ := NewSource("request", path)
	s.cache["old"] = cachedOrigin{Data: fixtureOrigin()}
	if _, err := s.ReadCycle(context.Background(), nil, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(s.cache) != 0 {
		t.Fatal("unrouted cache retained")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate((256 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err = NewSource("request", path); err == nil {
		t.Fatal("oversized cache read")
	}
}
