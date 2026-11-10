package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/credentialguard"
	"github.com/mvanhorn/agentcookie/internal/protocol"
	"github.com/mvanhorn/agentcookie/internal/transport"
)

func TestGeckoStorageReadScopeHonorsCookieOnlyAndSkip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envSkipLocalStorage, "")
	old := sourceSkipLocalStorage
	sourceSkipLocalStorage = false
	defer func() { sourceSkipLocalStorage = old }()
	if scope := geckoReadScope(configuredGeckoScope(context.Background(), false)); scope.LocalStorage || scope.IndexedDB {
		t.Fatal("cookie-only acquired storage", scope)
	}
	if scope := geckoReadScope(configuredGeckoScope(context.Background(), true)); !scope.LocalStorage || !scope.IndexedDB {
		t.Fatal("normal Gecko cycle omitted storage", scope)
	}
	t.Setenv(envSkipLocalStorage, "1")
	if scope := geckoReadScope(configuredGeckoScope(context.Background(), true)); scope.LocalStorage || !scope.IndexedDB {
		t.Fatal("skip-LS changed IDB authorization", scope)
	}
	if scope := geckoReadScope(withGeckoStoragePreview(context.Background(), true, true)); !scope.LocalStorage || !scope.IndexedDB {
		t.Fatal("explicit preview scope missing", scope)
	}
}

func TestStoragePipelineUsesStructuredRecordsAndCompleteRevocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(credentialguard.ForbidRealProviders())
	writeCLIFile(t, filepath.Join(home, "blocklist.yaml"), "version: 1\npolicy: blocklist\ndomains: []\n")
	mem := &destinationMemory{values: map[string]string{}}
	mux, restore, err := HermeticStorageSinkForTesting(home, "fixture-secret", mem)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	str := func(value string) json.RawMessage {
		raw, _ := json.Marshal(map[string]string{"t": "str", "v": value})
		return raw
	}
	origin := protocol.StorageOrigin{Origin: "https://fixture.test", LocalStorageStatus: protocol.SurfaceStatus{State: protocol.Complete}, IndexedDBStatus: protocol.SurfaceStatus{State: protocol.Complete}, LocalStorage: []protocol.LocalStorageItem{{Key: str("auth"), Value: str("token")}}, IndexedDB: []protocol.IDBDatabase{{Name: str("db"), Version: 1, Status: protocol.SurfaceStatus{State: protocol.Complete}, Stores: []protocol.IDBStore{{Name: str("tokens"), KeyPath: json.RawMessage("null"), Records: []protocol.IDBRecord{{Key: str("auth"), Value: str("token")}}}}}}}
	send := func(generation uint64) {
		t.Helper()
		env := protocol.SyncEnvelope{ProtocolVersion: 3, SourceHostname: "fixture", Destination: "fixture", Sequence: int64(generation), Snapshot: &protocol.SnapshotMeta{Generation: generation, Surfaces: map[protocol.Surface]protocol.SurfaceStatus{protocol.Cookies: {State: protocol.Complete}, protocol.LocalStorage: {State: protocol.Complete}, protocol.IndexedDB: {State: protocol.Complete}}}, Storage: &protocol.StorageRecords{Version: 1, Origins: []protocol.StorageOrigin{origin}}}
		raw, err := marshalBoundedEnvelope(env)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := transport.SealWithSecret(raw, "fixture-secret")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/sync", bytes.NewReader(sealed))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var reply syncReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != 200 || len(reply.Destinations) != 1 || !reply.Destinations[0].Ready {
			t.Fatalf("sink: %d %s (%v)", rec.Code, rec.Body.String(), err)
		}
	}
	send(1)
	if len(mem.values) != 3 {
		t.Fatal("structured LS/IDB/schema not delivered", mem.values)
	}
	origin.LocalStorage = nil
	origin.IndexedDB = nil
	send(2)
	if len(mem.values) != 0 {
		t.Fatal("complete storage revocation failed", mem.values)
	}
}
