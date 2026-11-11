package storagecodec

import (
	"time"

	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRejectsMalformedGraphs(t *testing.T) {
	for _, s := range []string{`{"t":"ref","id":1}`, `{"t":"str","v":"\ud800"}`, `{"t":"str16","v":[65536]}`, `{"t":"arr","id":1,"len":4294967295,"items":[]}`, `{"t":"obj","id":2,"props":[]}`, `{"t":"buf","id":1,"b64":"?"}`, `{"t":"num","v":null}`, `{"t":"num","v":-0}`, `{"t":"regexp","id":1,"src":{"t":"str","v":"x"},"flags":"uu"}`} {
		if Validate(json.RawMessage(s)) == nil {
			t.Errorf("accepted %s", s)
		}
	}
}
func TestValidateSupportedGraphs(t *testing.T) {
	for _, s := range []string{`{"t":"str","v":"a\ud83d\ude00"}`, `{"t":"str16","v":[55296,120,56320]}`, `{"t":"obj","id":1,"props":[[{"t":"str","v":"self"},{"t":"ref","id":1}]]}`, `{"t":"view","id":1,"kind":"Uint8Array","buf":{"t":"buf","id":2,"b64":"AQID"},"off":0,"len":3}`, `{"t":"bigint","v":"12345678901234567890"}`} {
		if err := Validate(json.RawMessage(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
}
func TestValidateDepth(t *testing.T) {
	s := `{"t":"null"}`
	for i := 65; i > 0; i-- {
		s = `{"t":"arr","id":` + jsonNumber(i) + `,"len":1,"items":[[0,` + s + `]]}`
	}
	if err := Validate(json.RawMessage(s)); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatal(err)
	}
}
func jsonNumber(i int) string { b, _ := json.Marshal(i); return string(b) }
func TestJSONSizeMatchesWireIncludingHTMLAndSchema(t *testing.T) {
	for _, value := range []any{map[string]any{"name": "漢<&\u2028😀", "raw": json.RawMessage(`{"t":"str","v":"<&漢"}`)}, []any{1e6, 1e-9, true, nil, "\n"}, recordsFixture(), cachedOrigin{Data: fixtureOrigin(), ReadAt: time.Unix(10, 3)}} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		size, err := JSONSize(value, len(encoded))
		if err != nil || size != len(encoded) {
			t.Fatalf("%T size %d != %d: %v", value, size, len(encoded), err)
		}
		if _, err = JSONSize(value, len(encoded)-1); err == nil {
			t.Fatal("cap not enforced")
		}
	}
}
