package protocol

import "encoding/json"

// Test-only shims so the test file can avoid pulling encoding/json into the
// production envelope.go.
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
