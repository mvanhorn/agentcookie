package chromeconn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsRemoteDebuggingPrefSet_Missing(t *testing.T) {
	dir := t.TempDir()
	on, err := IsRemoteDebuggingPrefSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("expected false for missing Local State")
	}
}

func TestIsRemoteDebuggingPrefSet_False(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{
		"devtools": map[string]any{
			"remote_debugging": map[string]any{
				"user-enabled": false,
			},
		},
	}
	writeJSON(t, filepath.Join(dir, "Local State"), state)
	on, err := IsRemoteDebuggingPrefSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Error("expected false")
	}
}

func TestIsRemoteDebuggingPrefSet_True(t *testing.T) {
	dir := t.TempDir()
	state := map[string]any{
		"devtools": map[string]any{
			"remote_debugging": map[string]any{
				"user-enabled": true,
			},
		},
		"other": "preserved",
	}
	writeJSON(t, filepath.Join(dir, "Local State"), state)
	on, err := IsRemoteDebuggingPrefSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Error("expected true")
	}
}

func TestSetRemoteDebuggingPref_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	original := map[string]any{
		"user_experience_metrics": map[string]any{"client_id": "abc-123"},
		"devtools": map[string]any{
			"adb_key": "secret-key-do-not-touch",
			"port_forwarding_config": map[string]any{
				"8080": "localhost:8080",
			},
		},
		"safebrowsing": "ok",
	}
	writeJSON(t, filepath.Join(dir, "Local State"), original)

	if err := SetRemoteDebuggingPref(dir); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "Local State"))
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	// New key set.
	dev := got["devtools"].(map[string]any)
	rd := dev["remote_debugging"].(map[string]any)
	if rd["user-enabled"] != true {
		t.Errorf("user-enabled: got %v want true", rd["user-enabled"])
	}
	// Sibling devtools keys preserved.
	if dev["adb_key"] != "secret-key-do-not-touch" {
		t.Errorf("adb_key clobbered: got %v", dev["adb_key"])
	}
	if pf, _ := dev["port_forwarding_config"].(map[string]any); pf["8080"] != "localhost:8080" {
		t.Errorf("port_forwarding_config clobbered: got %v", dev["port_forwarding_config"])
	}
	// Top-level siblings preserved.
	if got["safebrowsing"] != "ok" {
		t.Errorf("safebrowsing clobbered: got %v", got["safebrowsing"])
	}
	if uem, _ := got["user_experience_metrics"].(map[string]any); uem["client_id"] != "abc-123" {
		t.Errorf("user_experience_metrics clobbered: got %v", got["user_experience_metrics"])
	}
}

func TestSetRemoteDebuggingPref_NoLocalStateFile(t *testing.T) {
	dir := t.TempDir()
	err := SetRemoteDebuggingPref(dir)
	if err == nil {
		t.Fatal("expected error for missing Local State")
	}
}

func TestSetRemoteDebuggingPref_BootstrapsMissingParents(t *testing.T) {
	// Real-world case: brand-new Chrome profile where devtools subtree
	// doesn't exist yet.
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "Local State"), map[string]any{
		"some_other_key": "ok",
	})

	if err := SetRemoteDebuggingPref(dir); err != nil {
		t.Fatal(err)
	}

	on, err := IsRemoteDebuggingPrefSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Error("expected true after Set on bare Local State")
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
