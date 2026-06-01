// Package secretsbus's v2 adoption manifest. See
// docs/spec-agentcookie-secrets-bus-v2-adoption.md for the format.
package secretsbus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ManifestV2 is the parsed v2 adoption manifest. Distinct from the v1
// per-CLI Manifest type which controls sync overrides inside
// ~/.agentcookie/secrets/<name>/manifest.toml. v2 manifests live in
// ~/.agentcookie/manifests/<name>.toml (and friends per spec section 2.2)
// and declare a project's participation in agentcookie sync.
type ManifestV2 struct {
	SchemaVersion int    `toml:"schema_version"`
	Name          string `toml:"name"`
	DisplayName   string `toml:"display_name"`
	Description   string `toml:"description,omitempty"`
	ProjectKind   string `toml:"project_kind,omitempty"`
	Homepage      string `toml:"homepage,omitempty"`
	SignedBy      string `toml:"signed_by,omitempty"` // reserved for v2.1

	Secrets ManifestV2Secrets `toml:"secrets"`
	Sync    ManifestV2Sync    `toml:"sync,omitempty"`

	// Aliases declares consumer-env-var -> synced-bus-key mappings so a CLI
	// that reads a different env var name than the key its secret was imported
	// under is wired automatically, with no per-user `secret alias` command.
	// Example: a Tesla CLI reads TESLA_AUTH_TOKEN but agentcookie imports the
	// bearer as OAUTH_BEARER, so the manifest declares
	//   [aliases]
	//   TESLA_AUTH_TOKEN = "OAUTH_BEARER"
	// `secret env` applies these live on every call (tracking refreshes), and
	// an explicit local `secret alias` still overrides a manifest alias.
	Aliases map[string]string `toml:"aliases,omitempty"`

	// Files declares carried-file items: arbitrary files (a multiline PEM,
	// a TOML config) that the bus carries from source to sink sealed and
	// materializes on the sink as a 0600 file under ~/.agentcookie/. Because
	// the wire envelope is a flat map[string]map[string]string, each file
	// rides as a SINGLE key whose value is the base64 encoding of the file
	// bytes (single-line, dotenv-safe). This is a NEW manifest construct that
	// COEXISTS with the single [secrets.*] block (it is not a second
	// [secrets.*] block). See spec section 5.4.
	Files []ManifestV2File `toml:"files,omitempty"`
}

// ManifestV2File is one carried-file item declared as a [[files]] array entry.
//
// Example:
//
//	[[files]]
//	source = "~/.config/tesla-pp-cli/config.toml"
//	key = "TESLA_CONFIG_TOML"
//	target = "tesla-pp-cli/config.toml"
//	optional = false
//
// The source file at Source is read and base64-encoded into the wire key Key.
// On the sink, the decoded bytes are written 0600 to Target, resolved relative
// to ~/.agentcookie/. Target must stay inside ~/.agentcookie/ (no traversal,
// no absolute paths). When Optional is true the item is opt-in: discovery does
// NOT carry it unless the user enables it (see CarryFiles' enabled set).
type ManifestV2File struct {
	// Source is the path to the file to read and base64 into the bus. May
	// start with ~/ (expanded against the user's home). Required.
	Source string `toml:"source"`
	// Key is the wire envelope key the base64 payload rides under. Must be a
	// valid env-var-shaped key. Required.
	Key string `toml:"key"`
	// Target is the materialization path on the sink, relative to
	// ~/.agentcookie/. Required. Must not contain ".." and must not be
	// absolute; the sink refuses to write outside ~/.agentcookie/.
	Target string `toml:"target"`
	// Optional marks the item as opt-in. When true, discovery does not carry
	// it unless the user explicitly enables it. Default false (carried).
	Optional bool `toml:"optional,omitempty"`
}

// ManifestV2Secrets carries exactly one of File / Command / Keychain.
// Multi-block declarations are a hard error at parse time.
type ManifestV2Secrets struct {
	File     *ManifestV2SecretsFile     `toml:"file,omitempty"`
	Command  *ManifestV2SecretsCommand  `toml:"command,omitempty"`  // reserved
	Keychain *ManifestV2SecretsKeychain `toml:"keychain,omitempty"` // reserved
}

// ManifestV2SecretsFile points at an env-shaped file the agent reads in place
// on every push.
type ManifestV2SecretsFile struct {
	Path string `toml:"path"`
}

// ManifestV2SecretsCommand is reserved for v2.1.
type ManifestV2SecretsCommand struct {
	Exec []string `toml:"exec"`
}

// ManifestV2SecretsKeychain is reserved for v2.1.
type ManifestV2SecretsKeychain struct {
	Service string `toml:"service"`
}

// ManifestV2Sync mirrors the v1 sync table shape.
type ManifestV2Sync struct {
	Default bool            `toml:"default"`
	Keys    map[string]bool `toml:"keys,omitempty"`
}

// ParseWarning carries a soft-warning message tied to a manifest path. The
// parser accumulates these for unknown fields, reserved-field usage, and
// other non-fatal anomalies.
type ParseWarning struct {
	Path    string
	Message string
}

func (w ParseWarning) String() string {
	return fmt.Sprintf("%s: %s", w.Path, w.Message)
}

// ErrManifestNotFound is returned by ParseManifestV2 when the path does not exist.
var ErrManifestNotFound = errors.New("agentcookie.toml not found at path")

// ParseManifestV2 reads, parses, and validates a v2 manifest from disk. The
// returned ManifestV2 has every field on the parsed struct. Soft warnings
// (unknown fields, deprecated fields) are returned as the second value; the
// parser does not fail on them. Hard errors (schema mismatch, name traversal,
// multi-block secrets) return a non-nil error.
func ParseManifestV2(path string) (*ManifestV2, []ParseWarning, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrManifestNotFound
		}
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseManifestV2Bytes(data, path)
}

// parseManifestV2Bytes is the byte-input variant used by tests and the PP CLI
// adapter (which synthesizes manifest bytes in memory).
func parseManifestV2Bytes(data []byte, sourcePath string) (*ManifestV2, []ParseWarning, error) {
	// First pass: typed decode. Use md.Undecoded() to find unknown fields.
	m := &ManifestV2{}
	md, err := toml.Decode(string(data), m)
	if err != nil {
		return nil, nil, fmt.Errorf("toml parse: %w", err)
	}

	var warnings []ParseWarning
	for _, k := range md.Undecoded() {
		key := k.String()
		// Nested keys under known optional/reserved tables aren't true unknowns.
		// Skip if the top-level key is one we recognize but is reserved (e.g.,
		// secrets.command.exec inside an unsupported but parsed block).
		if strings.HasPrefix(key, "secrets.command") || strings.HasPrefix(key, "secrets.keychain") {
			continue
		}
		warnings = append(warnings, ParseWarning{
			Path:    sourcePath,
			Message: fmt.Sprintf("unknown field %q; ignored", key),
		})
	}

	// Second pass: map decode to detect explicit-vs-omitted sync.default.
	var raw map[string]interface{}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, nil, fmt.Errorf("toml map parse: %w", err)
	}

	if err := validateManifestV2(m, sourcePath); err != nil {
		return nil, warnings, err
	}

	// Apply sync defaults. sync.default omitted -> true per spec section 6.
	if syncRaw, ok := raw["sync"].(map[string]interface{}); ok {
		if _, ok := syncRaw["default"]; !ok {
			m.Sync.Default = true
		}
	} else {
		// Whole [sync] table omitted -> sync.default = true.
		m.Sync.Default = true
	}

	if m.SignedBy != "" {
		warnings = append(warnings, ParseWarning{
			Path:    sourcePath,
			Message: "signed_by field is reserved for v2.1; ignored in v2.0",
		})
	}

	return m, warnings, nil
}

func validateManifestV2(m *ManifestV2, sourcePath string) error {
	if m.SchemaVersion != 2 {
		if m.SchemaVersion == 1 {
			return fmt.Errorf("schema_version=1 is the v1 per-CLI sync override format, not the v2 adoption manifest; see docs/spec-agentcookie-secrets-bus-v1.md")
		}
		return fmt.Errorf("schema_version must be 2 (got %d); see docs/spec-agentcookie-secrets-bus-v2-adoption.md", m.SchemaVersion)
	}
	if m.Name == "" {
		return errors.New("name is required")
	}
	if strings.Contains(m.Name, "..") {
		return fmt.Errorf("name %q contains path-traversal segment", m.Name)
	}
	if !validCLIName(m.Name) {
		return fmt.Errorf("name %q does not match spec section 3.1 (lowercase, alphanumeric+hyphens, 1-64 chars)", m.Name)
	}
	if m.DisplayName == "" {
		return errors.New("display_name is required")
	}
	if len(m.DisplayName) > 200 {
		return fmt.Errorf("display_name exceeds 200 chars")
	}
	if len(m.Description) > 200 {
		return fmt.Errorf("description exceeds 200 chars")
	}

	if m.ProjectKind != "" {
		switch m.ProjectKind {
		case "cli", "skill", "service", "other":
		default:
			return fmt.Errorf("project_kind must be one of cli|skill|service|other (got %q)", m.ProjectKind)
		}
	}

	// Exactly one [secrets.*] block.
	srcCount := 0
	if m.Secrets.File != nil {
		srcCount++
	}
	if m.Secrets.Command != nil {
		srcCount++
	}
	if m.Secrets.Keychain != nil {
		srcCount++
	}
	if srcCount == 0 {
		return errors.New("exactly one [secrets.*] block required; none found")
	}
	if srcCount > 1 {
		return fmt.Errorf("exactly one [secrets.*] block required; %d found", srcCount)
	}

	if m.Secrets.Command != nil {
		return errors.New("[secrets.command] source kind not yet supported in v2.0 (reserved for v2.1)")
	}
	if m.Secrets.Keychain != nil {
		return errors.New("[secrets.keychain] source kind not yet supported in v2.0 (reserved for v2.1)")
	}

	if m.Secrets.File != nil {
		if m.Secrets.File.Path == "" {
			return errors.New("[secrets.file].path is required")
		}
		if strings.Contains(m.Secrets.File.Path, "..") {
			return fmt.Errorf("[secrets.file].path %q contains path-traversal segment", m.Secrets.File.Path)
		}
	}

	for declared, stored := range m.Aliases {
		if !validEnvKey(declared) {
			return fmt.Errorf("[aliases] key %q is not a valid env var name (A-Z, 0-9, underscore; not starting with a digit)", declared)
		}
		if !validEnvKey(stored) {
			return fmt.Errorf("[aliases] %q maps to %q, which is not a valid env var name", declared, stored)
		}
	}

	seenFileKeys := map[string]bool{}
	for i := range m.Files {
		f := &m.Files[i]
		if f.Source == "" {
			return fmt.Errorf("[[files]] item %d: source is required", i)
		}
		if strings.Contains(f.Source, "..") {
			return fmt.Errorf("[[files]] item %d: source %q contains path-traversal segment", i, f.Source)
		}
		if f.Key == "" {
			return fmt.Errorf("[[files]] item %d: key is required", i)
		}
		if !validEnvKey(f.Key) {
			return fmt.Errorf("[[files]] item %d: key %q is not a valid env var name (A-Z, 0-9, underscore; not starting with a digit)", i, f.Key)
		}
		if seenFileKeys[f.Key] {
			return fmt.Errorf("[[files]] duplicate key %q", f.Key)
		}
		seenFileKeys[f.Key] = true
		if err := validateMaterializeTarget(f.Target); err != nil {
			return fmt.Errorf("[[files]] item %d (key %q): %w", i, f.Key, err)
		}
	}

	return nil
}

// validateMaterializeTarget enforces that a carried-file target is a relative
// path that stays inside ~/.agentcookie/ after cleaning: non-empty, not
// absolute, no ".." traversal segment, and no parent escape. This mirrors the
// defense-in-depth posture of the [secrets.file].path validator and is the
// authoritative check the sink also re-applies before writing.
func validateMaterializeTarget(target string) error {
	if target == "" {
		return errors.New("target is required")
	}
	if filepath.IsAbs(target) {
		return fmt.Errorf("target %q must be relative to ~/.agentcookie/, not absolute", target)
	}
	if strings.Contains(target, "..") {
		return fmt.Errorf("target %q contains path-traversal segment", target)
	}
	// Defense in depth: after cleaning, the path must not escape the root.
	clean := filepath.Clean(target)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("target %q escapes ~/.agentcookie/", target)
	}
	if filepath.IsAbs(clean) {
		return fmt.Errorf("target %q resolves to an absolute path", target)
	}
	return nil
}

// validEnvKey reports whether s is a shell-safe environment variable name:
// an initial letter or underscore followed by letters, digits, or underscores.
// Used to validate manifest [aliases] entries (both the declared consumer var
// and the synced bus key it maps from).
func validEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		isLetter := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !isLetter && r != '_' {
				return false
			}
			continue
		}
		if !isLetter && !isDigit && r != '_' {
			return false
		}
	}
	return true
}

// ResolveSecretsPath expands ~/ in [secrets.file].path against the given
// homeDir. Absolute paths are returned as-is. Returns an empty string if the
// manifest has no [secrets.file] block.
func (m *ManifestV2) ResolveSecretsPath(homeDir string) string {
	if m.Secrets.File == nil {
		return ""
	}
	p := m.Secrets.File.Path
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir, p[2:])
	}
	if p == "~" {
		return homeDir
	}
	return p
}

// SyncDefault returns the resolved default-ship behavior. Omitted -> true.
func (m *ManifestV2) SyncDefault() bool {
	return m.Sync.Default
}

// ShouldShipKey returns whether a given key should be included in the wire
// envelope according to the manifest's sync policy. Spec section 6: per-key
// entries override the default.
func (m *ManifestV2) ShouldShipKey(key string) bool {
	if override, ok := m.Sync.Keys[key]; ok {
		return override
	}
	return m.SyncDefault()
}
