package agentcookieadoption

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Manifest is the v2 adoption manifest. Mirrors the schema in
// docs/spec-agentcookie-secrets-bus-v2-adoption.md and the internal
// ManifestV2 type in internal/secretsbus/manifest_v2.go.
type Manifest struct {
	SchemaVersion int     `toml:"schema_version"`
	Name          string  `toml:"name"`
	DisplayName   string  `toml:"display_name"`
	Description   string  `toml:"description,omitempty"`
	ProjectKind   string  `toml:"project_kind,omitempty"`
	Homepage      string  `toml:"homepage,omitempty"`
	SignedBy      string  `toml:"signed_by,omitempty"`
	Secrets       Secrets `toml:"secrets"`
	Sync          Sync    `toml:"sync,omitempty"`
}

// Secrets carries the one-of source kind. Exactly one field must be non-nil.
type Secrets struct {
	File     *SecretsFile     `toml:"file,omitempty"`
	Command  *SecretsCommand  `toml:"command,omitempty"`
	Keychain *SecretsKeychain `toml:"keychain,omitempty"`
}

type SecretsFile struct {
	Path string `toml:"path"`
}

type SecretsCommand struct {
	Exec []string `toml:"exec"`
}

type SecretsKeychain struct {
	Service string `toml:"service"`
}

type Sync struct {
	Default bool            `toml:"default"`
	Keys    map[string]bool `toml:"keys,omitempty"`
}

// Validate applies the same rules the discovery loop applies at parse time.
// Authors should call this before WriteTo.
func Validate(m *Manifest) error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	if m.SchemaVersion != 2 {
		return fmt.Errorf("schema_version must be 2 (got %d)", m.SchemaVersion)
	}
	if m.Name == "" {
		return errors.New("name is required")
	}
	if strings.Contains(m.Name, "..") {
		return fmt.Errorf("name %q contains path-traversal segment", m.Name)
	}
	if !validSlug(m.Name) {
		return fmt.Errorf("name %q must be lowercase, alphanumeric+hyphens, 1-64 chars, no leading/trailing hyphen", m.Name)
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
	srcCount := 0
	if m.Secrets.File != nil {
		srcCount++
		if m.Secrets.File.Path == "" {
			return errors.New("[secrets.file].path is required")
		}
		if strings.Contains(m.Secrets.File.Path, "..") {
			return fmt.Errorf("[secrets.file].path %q contains path-traversal segment", m.Secrets.File.Path)
		}
	}
	if m.Secrets.Command != nil {
		srcCount++
	}
	if m.Secrets.Keychain != nil {
		srcCount++
	}
	if srcCount == 0 {
		return errors.New("exactly one [secrets.*] block required; none provided")
	}
	if srcCount > 1 {
		return fmt.Errorf("exactly one [secrets.*] block required; %d provided", srcCount)
	}
	return nil
}

// WriteTo renders m as canonical TOML and writes to the given path. Output
// is deterministic: keys within [sync.keys] are sorted alphabetically; the
// section ordering follows the spec's preferred shape.
func WriteTo(m *Manifest, path string) error {
	if err := Validate(m); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	body := Render(m)
	return os.WriteFile(path, []byte(body), 0o644)
}

// Render produces the canonical TOML string for m. Separated from WriteTo
// so callers (and tests) can inspect the output without touching disk.
func Render(m *Manifest) string {
	var b strings.Builder
	b.WriteString("# agentcookie.toml: secrets-bus adoption manifest v2\n")
	b.WriteString("# See docs/spec-agentcookie-secrets-bus-v2-adoption.md\n")
	fmt.Fprintf(&b, "schema_version = %d\n", m.SchemaVersion)
	fmt.Fprintf(&b, "name = %q\n", m.Name)
	fmt.Fprintf(&b, "display_name = %q\n", m.DisplayName)
	if m.Description != "" {
		fmt.Fprintf(&b, "description = %q\n", m.Description)
	}
	if m.ProjectKind != "" {
		fmt.Fprintf(&b, "project_kind = %q\n", m.ProjectKind)
	}
	if m.Homepage != "" {
		fmt.Fprintf(&b, "homepage = %q\n", m.Homepage)
	}
	if m.SignedBy != "" {
		fmt.Fprintf(&b, "signed_by = %q\n", m.SignedBy)
	}
	b.WriteString("\n")
	if m.Secrets.File != nil {
		b.WriteString("[secrets.file]\n")
		fmt.Fprintf(&b, "path = %q\n", m.Secrets.File.Path)
		b.WriteString("\n")
	}
	if m.Secrets.Command != nil {
		b.WriteString("[secrets.command]\n")
		fmt.Fprintf(&b, "exec = %q\n", m.Secrets.Command.Exec)
		b.WriteString("\n")
	}
	if m.Secrets.Keychain != nil {
		b.WriteString("[secrets.keychain]\n")
		fmt.Fprintf(&b, "service = %q\n", m.Secrets.Keychain.Service)
		b.WriteString("\n")
	}
	// [sync] always emitted when either default differs from omitted-default
	// (true) OR keys are set.
	emitSync := !m.Sync.Default || len(m.Sync.Keys) > 0
	if emitSync {
		b.WriteString("[sync]\n")
		fmt.Fprintf(&b, "default = %t\n", m.Sync.Default)
		b.WriteString("\n")
		if len(m.Sync.Keys) > 0 {
			b.WriteString("[sync.keys]\n")
			keys := make([]string, 0, len(m.Sync.Keys))
			for k := range m.Sync.Keys {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, "%s = %t\n", k, m.Sync.Keys[k])
			}
		}
	}
	return b.String()
}

// validSlug mirrors internal/secretsbus.validCLIName for the public API
// surface. The two are intentionally duplicated so this package has no
// internal-package dependency.
func validSlug(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
