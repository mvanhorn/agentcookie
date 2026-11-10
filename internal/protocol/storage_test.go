package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestV3OptionalFieldsPreserveV2Envelope(t *testing.T) {
	raw, err := json.Marshal(SyncEnvelope{ProtocolVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"snapshot", "storage", "destination", "source_browser", "reader_mode"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("unexpected optional field %s", key)
		}
	}
}
func TestStorageIRPreservesUTF16AndKeyTypes(t *testing.T) {
	original := `{"version":1,"origins":[{"origin":"http://localhost:1234","container":2,"local_storage":[{"key":{"t":"str16","v":[55296]},"value":{"t":"str","v":"fixture"}}],"local_storage_status":{"state":"complete"},"indexed_db_status":{"state":"unsupported"}}]}`
	var ir StorageRecords
	if err := json.Unmarshal([]byte(original), &ir); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ir)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != original {
		t.Fatalf("lossy IR roundtrip: %s", raw)
	}
}

func TestIDBIdentifiersPreserveLoneSurrogates(t *testing.T) {
	raw := []byte(`{"name":{"t":"str16","v":[55296]},"version":1,"stores":[{"name":{"t":"str16","v":[56320]},"keyPath":null,"autoIncrement":false,"indexes":[{"name":{"t":"str16","v":[55297]},"keyPath":{"t":"str","v":"id"},"unique":false,"multiEntry":false}],"records":[]}],"status":{"state":"complete"}}`)
	var db IDBDatabase
	if err := json.Unmarshal(raw, &db); err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(db)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("lossy IndexedDB identifiers: %s", got)
	}
}
