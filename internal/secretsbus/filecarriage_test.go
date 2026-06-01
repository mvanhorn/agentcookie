package secretsbus

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pemFixture is a representative multiline secret that CANNOT ride as a raw
// KEY=VALUE value: an EC private key PEM.
const pemFixture = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIExampleExampleExampleExampleExampleExampleExampleoAoGCCqG
SM49AwEHoUQDQgAEExampleExampleExampleExampleExampleExampleExampleEx
ampleExampleExampleExampleExampleExampleExampleExampleExample==
-----END EC PRIVATE KEY-----
`

func TestCarryFiles_Base64RoundTrip(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, ".config", "tesla-pp-cli", "fleet-key.pem")
	writeFile(t, src, pemFixture)

	files := []ManifestV2File{{
		Source: "~/.config/tesla-pp-cli/fleet-key.pem",
		Key:    "TESLA_FLEET_KEY_PEM",
		Target: "tesla-pp-cli/fleet-key.pem",
	}}
	carried, errs := CarryFiles(files, nil, home)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	b64, ok := carried["TESLA_FLEET_KEY_PEM"]
	if !ok {
		t.Fatal("payload key not carried")
	}
	if strings.ContainsAny(b64, "\n") {
		t.Error("base64 value must be single-line")
	}
	if got := carried[CarryFileKey("TESLA_FLEET_KEY_PEM")]; got != "tesla-pp-cli/fleet-key.pem" {
		t.Errorf("companion target = %q", got)
	}

	// envelope round trip: bytes in == bytes out after encode -> decode.
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded, []byte(pemFixture)) {
		t.Error("round-trip mismatch: decoded bytes != source bytes")
	}
}

func TestMaterializeFiles_Writes0600AndRoundTrips(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"TESLA_FLEET_KEY_PEM":               base64.StdEncoding.EncodeToString([]byte(pemFixture)),
		CarryFileKey("TESLA_FLEET_KEY_PEM"): "tesla-pp-cli/fleet-key.pem",
		"OTHER_ENV":                         "keepme",
	}
	res, consumed, errs := MaterializeFiles(home, "tesla-pp-cli", env)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if res.FilesWritten != 1 {
		t.Fatalf("FilesWritten = %d, want 1", res.FilesWritten)
	}
	dest := filepath.Join(home, ".agentcookie", "tesla-pp-cli", "fleet-key.pem")
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, []byte(pemFixture)) {
		t.Error("materialized content != source content")
	}
	// Both the payload key and its companion are consumed; the unrelated env
	// var is not.
	if !consumed["TESLA_FLEET_KEY_PEM"] || !consumed[CarryFileKey("TESLA_FLEET_KEY_PEM")] {
		t.Errorf("payload + companion should be consumed: %v", consumed)
	}
	if consumed["OTHER_ENV"] {
		t.Error("unrelated env var should not be consumed")
	}
}

func TestMaterializeFiles_OverwriteIsAtomicAndValuePreserved(t *testing.T) {
	home := t.TempDir()
	dest := filepath.Join(home, ".agentcookie", "tesla-pp-cli", "config.toml")
	writeFile(t, dest, "old contents\n")

	newContent := "[fleet]\nbearer = \"fresh\"\n"
	env := map[string]string{
		"TESLA_CONFIG":               base64.StdEncoding.EncodeToString([]byte(newContent)),
		CarryFileKey("TESLA_CONFIG"): "tesla-pp-cli/config.toml",
	}
	res, _, errs := MaterializeFiles(home, "tesla-pp-cli", env)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if res.FilesWritten != 1 {
		t.Fatalf("FilesWritten = %d", res.FilesWritten)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != newContent {
		t.Errorf("overwrite content = %q, want %q", got, newContent)
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("overwritten file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestMaterializeFiles_RefusesTargetEscapingRoot(t *testing.T) {
	home := t.TempDir()
	cases := map[string]string{
		"traversal":      "../escape.pem",
		"deep traversal": "tesla/../../escape.pem",
		"absolute":       "/etc/cron.d/evil",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{
				"PAYLOAD":               base64.StdEncoding.EncodeToString([]byte("x")),
				CarryFileKey("PAYLOAD"): target,
			}
			res, _, errs := MaterializeFiles(home, "cli", env)
			if res.FilesWritten != 0 {
				t.Errorf("should not write for target %q", target)
			}
			if len(errs) == 0 {
				t.Errorf("expected refusal error for target %q", target)
			}
			// Nothing escaped the home dir.
			if _, err := os.Stat(filepath.Join(home, "..", "escape.pem")); err == nil {
				t.Errorf("file escaped root for target %q", target)
			}
		})
	}
}

func TestMaterializeFiles_RejectsInvalidBase64(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"PAYLOAD":               "!!!not base64!!!",
		CarryFileKey("PAYLOAD"): "cli/file.bin",
	}
	res, _, errs := MaterializeFiles(home, "cli", env)
	if res.FilesWritten != 0 {
		t.Error("invalid base64 should not write")
	}
	if len(errs) == 0 {
		t.Error("expected base64 error")
	}
}

func TestMaterializeFiles_RejectsOversizedPayload(t *testing.T) {
	home := t.TempDir()
	big := bytes.Repeat([]byte("A"), maxCarriedFileBytes+1)
	env := map[string]string{
		"PAYLOAD":               base64.StdEncoding.EncodeToString(big),
		CarryFileKey("PAYLOAD"): "cli/big.bin",
	}
	res, _, errs := MaterializeFiles(home, "cli", env)
	if res.FilesWritten != 0 {
		t.Error("oversized payload should not write")
	}
	if len(errs) == 0 {
		t.Error("expected oversize error")
	}
	if _, err := os.Stat(filepath.Join(home, ".agentcookie", "cli", "big.bin")); err == nil {
		t.Error("oversized file should not exist")
	}
}

func TestCarryFiles_OptionalNotCarriedUnlessEnabled(t *testing.T) {
	home := t.TempDir()
	srcDefault := filepath.Join(home, "default.txt")
	srcOpt := filepath.Join(home, "opt.txt")
	writeFile(t, srcDefault, "default")
	writeFile(t, srcOpt, "optional")

	files := []ManifestV2File{
		{Source: srcDefault, Key: "DEFAULT_FILE", Target: "cli/default.txt"},
		{Source: srcOpt, Key: "OPT_FILE", Target: "cli/opt.txt", Optional: true},
	}

	// No opt-in: only the default item is carried.
	carried, errs := CarryFiles(files, nil, home)
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if _, ok := carried["DEFAULT_FILE"]; !ok {
		t.Error("default item should be carried")
	}
	if _, ok := carried["OPT_FILE"]; ok {
		t.Error("optional item should NOT be carried without opt-in")
	}

	// Opt-in: both carried.
	carried2, errs2 := CarryFiles(files, map[string]bool{"OPT_FILE": true}, home)
	if len(errs2) != 0 {
		t.Fatalf("errors: %v", errs2)
	}
	if _, ok := carried2["OPT_FILE"]; !ok {
		t.Error("opted-in optional item should be carried")
	}
}

func TestCarryFiles_MissingSourceIsError(t *testing.T) {
	home := t.TempDir()
	files := []ManifestV2File{{Source: "~/does-not-exist.pem", Key: "K", Target: "cli/k.pem"}}
	carried, errs := CarryFiles(files, nil, home)
	if len(errs) == 0 {
		t.Error("missing source should error")
	}
	if _, ok := carried["K"]; ok {
		t.Error("missing source should not be carried")
	}
}

func TestLoadEnabledFileKeys(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".agentcookie", "file-optin", "tesla-pp-cli.keys")
	writeFile(t, p, "# opt-in keys\nTESLA_FLEET_KEY_PEM\n\n  SPACED_KEY  \n")
	enabled := LoadEnabledFileKeys(home, "tesla-pp-cli")
	if !enabled["TESLA_FLEET_KEY_PEM"] {
		t.Error("declared key should be enabled")
	}
	if !enabled["SPACED_KEY"] {
		t.Error("whitespace-trimmed key should be enabled")
	}
	if enabled["#"] {
		t.Error("comment should be ignored")
	}

	// Missing file: empty set.
	if len(LoadEnabledFileKeys(home, "no-such-cli")) != 0 {
		t.Error("missing opt-in file should yield empty set")
	}
}

func TestSafeJoinUnderRoot(t *testing.T) {
	root := "/home/u/.agentcookie"
	ok := []string{"a.pem", "tesla/config.toml", "a/b/c.bin"}
	for _, rel := range ok {
		if _, err := safeJoinUnderRoot(root, rel); err != nil {
			t.Errorf("safeJoinUnderRoot(%q) unexpected err: %v", rel, err)
		}
	}
	bad := []string{"../x", "a/../../x", "/etc/passwd", ".", ""}
	for _, rel := range bad {
		if _, err := safeJoinUnderRoot(root, rel); err == nil {
			t.Errorf("safeJoinUnderRoot(%q) should have errored", rel)
		}
	}
}
