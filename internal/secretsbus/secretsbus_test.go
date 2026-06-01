package secretsbus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadPayload_MissingRoot(t *testing.T) {
	home := t.TempDir()
	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if !p.IsEmpty() {
		t.Fatalf("missing root should produce empty payload, got %d clis", len(p.CLIs))
	}
}

func TestLoadPayload_SimpleEnvFile(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(SecretsRoot(home), "tesla-pp-cli", "secrets.env"),
		"TESLA_OAUTH_BEARER=ey-test\nTESLA_OAUTH_REFRESH=ey-refresh\n")

	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	got := p.CLIs["tesla-pp-cli"]
	if got["TESLA_OAUTH_BEARER"] != "ey-test" {
		t.Errorf("BEARER mismatch: %v", got)
	}
	if got["TESLA_OAUTH_REFRESH"] != "ey-refresh" {
		t.Errorf("REFRESH mismatch: %v", got)
	}
}

func TestLoadPayload_ManifestSyncFalseSkipsKey(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "demo-cli")
	writeFile(t, filepath.Join(cliDir, "secrets.env"),
		"PUBLIC_API_KEY=abc\nPRIVATE_KEY=xyz\n")
	writeFile(t, filepath.Join(cliDir, "manifest.toml"),
		`schema_version = 1
display_name = "demo"
[sync]
default = true
[sync.keys]
PRIVATE_KEY = false
`)

	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	cli := p.CLIs["demo-cli"]
	if _, leaked := cli["PRIVATE_KEY"]; leaked {
		t.Errorf("PRIVATE_KEY should be filtered out: %v", cli)
	}
	if cli["PUBLIC_API_KEY"] != "abc" {
		t.Errorf("PUBLIC_API_KEY should pass through: %v", cli)
	}
}

func TestLoadPayload_ManifestDefaultFalseSkipsAll(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "demo-cli")
	writeFile(t, filepath.Join(cliDir, "secrets.env"), "A=1\nB=2\n")
	writeFile(t, filepath.Join(cliDir, "manifest.toml"),
		`schema_version = 1
[sync]
default = false
`)

	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if _, present := p.CLIs["demo-cli"]; present {
		t.Errorf("default=false should drop the entire CLI from the payload")
	}
}

func TestLoadPayload_ManifestKeysOverrideFalseDefault(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "demo-cli")
	writeFile(t, filepath.Join(cliDir, "secrets.env"), "A=1\nB=2\n")
	writeFile(t, filepath.Join(cliDir, "manifest.toml"),
		`schema_version = 1
[sync]
default = false
[sync.keys]
A = true
`)

	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	cli := p.CLIs["demo-cli"]
	if cli["A"] != "1" {
		t.Errorf("A should be allowed by per-key override: %v", cli)
	}
	if _, present := cli["B"]; present {
		t.Errorf("B should still be filtered (no override, default=false): %v", cli)
	}
}

func TestLoadPayload_OversizeFileSkipped(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "huge-cli")
	huge := strings.Repeat("BIG_KEY="+strings.Repeat("x", 100)+"\n", 3000)
	writeFile(t, filepath.Join(cliDir, "secrets.env"), huge)

	p, errs := LoadPayload(home)
	if len(errs) == 0 {
		t.Fatalf("expected an error about oversize file")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "over the") || strings.Contains(e.Error(), "limit") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected size-limit error, got: %v", errs)
	}
	if _, present := p.CLIs["huge-cli"]; present {
		t.Errorf("huge cli should not be in payload")
	}
}

func TestLoadPayload_InvalidCLINameSkipped(t *testing.T) {
	home := t.TempDir()
	// "Foo" violates lowercase rule
	writeFile(t, filepath.Join(SecretsRoot(home), "Foo", "secrets.env"), "A=1\n")
	p, errs := LoadPayload(home)
	if _, present := p.CLIs["Foo"]; present {
		t.Errorf("invalid cli-name dir should be skipped")
	}
	if len(errs) == 0 {
		t.Errorf("expected a non-fatal error about invalid name")
	}
}

func TestLoadPayload_ManifestOnlySkippedSilently(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "demo-cli")
	writeFile(t, filepath.Join(cliDir, "manifest.toml"),
		`schema_version = 1
[sync]
default = true
`)

	p, errs := LoadPayload(home)
	if len(errs) > 0 {
		t.Errorf("manifest-only should be silent, got errors: %v", errs)
	}
	if _, present := p.CLIs["demo-cli"]; present {
		t.Errorf("manifest-only (no env file) should not produce a CLI entry")
	}
}

func TestLoadPayload_MalformedManifest(t *testing.T) {
	home := t.TempDir()
	cliDir := filepath.Join(SecretsRoot(home), "broken-cli")
	writeFile(t, filepath.Join(cliDir, "secrets.env"), "A=1\n")
	writeFile(t, filepath.Join(cliDir, "manifest.toml"), "not = toml = invalid\n")

	p, errs := LoadPayload(home)
	if len(errs) == 0 {
		t.Errorf("malformed manifest should produce an error")
	}
	if _, present := p.CLIs["broken-cli"]; present {
		t.Errorf("malformed manifest should drop the CLI from payload")
	}
}

func TestParseEnvFile_Comments(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, "# header comment\nKEY1=value1\n\n# another\nKEY2=value2\n")
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["KEY1"] != "value1" || got["KEY2"] != "value2" {
		t.Errorf("comments should be ignored, got: %v", got)
	}
}

func TestParseEnvFile_QuotesStripped(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, `DQ="quoted value"
SQ='single quoted'
BARE=plain
`)
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["DQ"] != "quoted value" {
		t.Errorf("DQ: %q", got["DQ"])
	}
	if got["SQ"] != "single quoted" {
		t.Errorf("SQ: %q", got["SQ"])
	}
	if got["BARE"] != "plain" {
		t.Errorf("BARE: %q", got["BARE"])
	}
}

func TestParseEnvFile_WhitespaceAroundEqualsRejected(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, "KEY = value\n")
	_, err := parseEnvFile(p)
	if err == nil {
		t.Fatalf("whitespace around = should be rejected")
	}
}

func TestParseEnvFile_MissingEqualsRejected(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, "KEY value without equals\n")
	_, err := parseEnvFile(p)
	if err == nil {
		t.Fatalf("missing = should be rejected")
	}
}

func TestParseEnvFile_BackslashContinuation(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, "LONG=part1\\\npart2\n")
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["LONG"] != "part1part2" {
		t.Errorf("backslash continuation: %q", got["LONG"])
	}
}

func TestParseEnvFile_ReservedKeysPassThrough(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "secrets.env")
	writeFile(t, p, "_unknown_legacy_field=somevalue\n_BIN_BLOB=YmFzZTY0\n")
	got, err := parseEnvFile(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["_unknown_legacy_field"] != "somevalue" {
		t.Errorf("reserved key should pass through: %v", got)
	}
	if got["_BIN_BLOB"] != "YmFzZTY0" {
		t.Errorf("_BIN_ key should pass through: %v", got)
	}
}

func TestValidCLIName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"foo", true},
		{"foo-bar", true},
		{"foo-pp-cli", true},
		{"foo123", true},
		{"123foo", true},
		{"", false},
		{"Foo", false},     // uppercase
		{"-foo", false},    // leading hyphen
		{"foo-", false},    // trailing hyphen
		{"foo.bar", false}, // dot
		{"foo/bar", false}, // slash
		{"foo_bar", false}, // underscore (per spec, only hyphen)
		{"..", false},      // traversal
	}
	for _, tc := range cases {
		got := validCLIName(tc.in)
		if got != tc.want {
			t.Errorf("validCLIName(%q): got %v, want %v", tc.in, got, tc.want)
		}
	}
}
