package secretsbus

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePayload_HappyPath(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"demo-cli": {"KEY1": "value1", "KEY2": "value2"},
	}
	result, errs := WritePayload(home, payload, false)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if result.CLIsWritten != 1 || result.KeysWritten != 2 || result.PlaintextWritten != 1 || result.SealedWritten != 0 {
		t.Errorf("result mismatch: %+v", result)
	}
	envPath := filepath.Join(SecretsRoot(home), "demo-cli", "secrets.env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected mode 0600, got %v", info.Mode().Perm())
	}
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), "KEY1=value1") || !strings.Contains(string(content), "KEY2=value2") {
		t.Errorf("content mismatch: %s", string(content))
	}
}

func TestWritePayload_MaterializesCarriedFileAndStripsKeys(t *testing.T) {
	home := t.TempDir()
	pem := "-----BEGIN EC PRIVATE KEY-----\nMHc...\n-----END EC PRIVATE KEY-----\n"
	payload := map[string]map[string]string{
		"tesla-pp-cli": {
			"TESLA_AUTH_TOKEN":            base64.StdEncoding.EncodeToString([]byte("bearer")), // ordinary env key (not a file)
			"FLEET_KEY_PEM":               base64.StdEncoding.EncodeToString([]byte(pem)),
			CarryFileKey("FLEET_KEY_PEM"): "tesla-pp-cli/fleet-key.pem",
		},
	}
	result, errs := WritePayload(home, payload, false)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if result.FilesMaterialized != 1 {
		t.Errorf("FilesMaterialized = %d, want 1", result.FilesMaterialized)
	}
	// Materialized 0600 under ~/.agentcookie/.
	dest := filepath.Join(home, ".agentcookie", "tesla-pp-cli", "fleet-key.pem")
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("materialized mode = %v, want 0600", info.Mode().Perm())
	}
	got, _ := os.ReadFile(dest)
	if string(got) != pem {
		t.Errorf("materialized content mismatch")
	}
	// The carried payload + companion are stripped from secrets.env; the
	// ordinary env key remains.
	envPath := filepath.Join(SecretsRoot(home), "tesla-pp-cli", "secrets.env")
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read secrets.env: %v", err)
	}
	if strings.Contains(string(content), "FLEET_KEY_PEM") {
		t.Errorf("carried file key leaked into secrets.env: %s", content)
	}
	if !strings.Contains(string(content), "TESLA_AUTH_TOKEN=") {
		t.Errorf("ordinary env key missing from secrets.env: %s", content)
	}
}

func TestWritePayload_NoFileKeysNoRegression(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"demo-cli": {"KEY1": "value1"},
	}
	result, errs := WritePayload(home, payload, false)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if result.FilesMaterialized != 0 {
		t.Errorf("FilesMaterialized = %d, want 0", result.FilesMaterialized)
	}
	if result.CLIsWritten != 1 || result.KeysWritten != 1 {
		t.Errorf("regression in non-file payload: %+v", result)
	}
	// No ~/.agentcookie sub-file beyond the secrets dir.
	if _, err := os.Stat(filepath.Join(home, ".agentcookie", "demo-cli")); err == nil {
		t.Error("unexpected materialized dir for non-file payload")
	}
}

func TestWritePayload_EmptyPayloadIsNoOp(t *testing.T) {
	home := t.TempDir()
	result, errs := WritePayload(home, nil, false)
	if len(errs) > 0 || result.CLIsWritten != 0 {
		t.Errorf("nil payload should be no-op, got %+v %v", result, errs)
	}
	result, errs = WritePayload(home, map[string]map[string]string{}, false)
	if len(errs) > 0 || result.CLIsWritten != 0 {
		t.Errorf("empty payload should be no-op, got %+v %v", result, errs)
	}
	// Root should not exist on no-op
	if _, err := os.Stat(SecretsRoot(home)); err == nil {
		t.Errorf("no-op should not create the secrets root")
	}
}

func TestWritePayload_RejectsInvalidCLIName(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"../etc/passwd": {"X": "Y"},
		"good-cli":      {"GOOD": "VAL"},
	}
	result, errs := WritePayload(home, payload, false)
	if result.CLIsWritten != 1 {
		t.Errorf("expected 1 good cli to land, got %d (errs=%v)", result.CLIsWritten, errs)
	}
	if _, err := os.Stat(filepath.Join(home, ".agentcookie", "secrets", "good-cli", "secrets.env")); err != nil {
		t.Errorf("good cli should be written: %v", err)
	}
	// Confirm the traversal path was not created.
	if _, err := os.Stat("/etc/passwd.agentcookie-test"); err == nil {
		t.Errorf("path traversal should have been blocked")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "invalid name") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected invalid-name error, got: %v", errs)
	}
}

func TestWritePayload_RejectsInvalidKeyNames(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"demo-cli": {
			"GOOD_KEY":   "value",
			"123starts":  "rejected",
			"has spaces": "rejected",
			"has-hyphen": "rejected",
		},
	}
	result, errs := WritePayload(home, payload, false)
	if result.KeysWritten != 1 {
		t.Errorf("only GOOD_KEY should write; got %d (errs=%v)", result.KeysWritten, errs)
	}
	content, _ := os.ReadFile(filepath.Join(SecretsRoot(home), "demo-cli", "secrets.env"))
	if strings.Contains(string(content), "123starts") || strings.Contains(string(content), "has-hyphen") || strings.Contains(string(content), "has spaces") {
		t.Errorf("invalid keys leaked into output: %s", string(content))
	}
}

func TestWritePayload_AtomicTmpCleanup(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"demo-cli": {"K": "V"},
	}
	_, errs := WritePayload(home, payload, false)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	// .tmp file should not survive the rename.
	tmpGlob, _ := filepath.Glob(filepath.Join(SecretsRoot(home), "demo-cli", "*.tmp"))
	if len(tmpGlob) > 0 {
		t.Errorf(".tmp files left behind: %v", tmpGlob)
	}
}

func TestWritePayload_SealingRequested_MasterKeyMissing(t *testing.T) {
	home := t.TempDir()
	payload := map[string]map[string]string{
		"demo-cli": {"K": "V"},
	}
	// Sealing requested but no master key in the keychain: should still
	// write plaintext and produce a non-fatal error.
	result, errs := WritePayload(home, payload, true)
	if result.PlaintextWritten != 1 {
		t.Errorf("plaintext should still write when master key missing, got %+v", result)
	}
	if result.SealedWritten != 0 {
		t.Errorf("sealed should be 0 when master key missing, got %d", result.SealedWritten)
	}
	if len(errs) == 0 {
		t.Errorf("expected a non-fatal error about missing master key")
	}
}

func TestRenderEnvFile_SortedDeterministic(t *testing.T) {
	out := string(renderEnvFile(map[string]string{
		"Z": "1",
		"A": "2",
		"M": "3",
	}))
	// Strip the header comments to check just the key order.
	lines := strings.Split(out, "\n")
	var keyLines []string
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		keyLines = append(keyLines, l)
	}
	if len(keyLines) != 3 || keyLines[0] != "A=2" || keyLines[1] != "M=3" || keyLines[2] != "Z=1" {
		t.Errorf("expected sorted A,M,Z, got: %v", keyLines)
	}
}

func TestAtomicWrite_BasicRoundTrip(t *testing.T) {
	tmpdir := t.TempDir()
	p := filepath.Join(tmpdir, "out.txt")
	if err := atomicWrite(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "hello" {
		t.Errorf("content: %q", got)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v", info.Mode().Perm())
	}
}
