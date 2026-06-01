package secretsbus

import (
	"encoding/json"
	"fmt"
	"os"
)

// ppCLIMetadata is the subset of .printing-press.json the adapter cares about.
// The full schema is owned by cli-printing-press; this struct only declares
// the fields we read, so unknown fields in the JSON are silently ignored by
// encoding/json.
type ppCLIMetadata struct {
	SchemaVersion   int                   `json:"schema_version"`
	APIName         string                `json:"api_name"`
	DisplayName     string                `json:"display_name"`
	CLIName         string                `json:"cli_name"`
	Description     string                `json:"description"`
	AuthEnvVars     []string              `json:"auth_env_vars"`
	AuthEnvVarSpecs []ppCLIAuthEnvVarSpec `json:"auth_env_var_specs"`
}

type ppCLIAuthEnvVarSpec struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive"`
	Description string `json:"description"`
}

// DeriveManifestFromPP reads a .printing-press.json file and synthesizes an
// in-memory v2 manifest pointing at the canonical PP CLI auth path.
// The manifest is never written to disk; it lives only for the duration of
// the source process. See spec section 7 for the field mapping.
func DeriveManifestFromPP(jsonPath string) (*ManifestV2, error) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", jsonPath, err)
	}
	var meta ppCLIMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", jsonPath, err)
	}
	if meta.CLIName == "" {
		return nil, fmt.Errorf("%s: cli_name is required for v2 derivation", jsonPath)
	}
	if !validCLIName(meta.CLIName) {
		return nil, fmt.Errorf("%s: cli_name %q does not match v2 slug rules", jsonPath, meta.CLIName)
	}

	m := &ManifestV2{
		SchemaVersion: 2,
		Name:          meta.CLIName,
		DisplayName:   meta.DisplayName,
		Description:   meta.Description,
		ProjectKind:   "cli",
		Secrets: ManifestV2Secrets{
			File: &ManifestV2SecretsFile{
				// Canonical PP CLI auth location per the U1 audit:
				// docs/audits/2026-05-22-pp-cli-auth-inventory.md.
				Path: fmt.Sprintf("~/.config/%s/config.toml", meta.CLIName),
			},
		},
		Sync: ManifestV2Sync{
			// Per spec section 7.1: only sensitive=true keys default-ship.
			// Non-sensitive keys get an explicit false override.
			Default: false,
			Keys:    map[string]bool{},
		},
	}
	if meta.DisplayName == "" {
		m.DisplayName = meta.CLIName
	}

	// Honor auth_env_var_specs when present; fall back to auth_env_vars
	// (which has no sensitive flag, treat all as default-shipped).
	if len(meta.AuthEnvVarSpecs) > 0 {
		for _, spec := range meta.AuthEnvVarSpecs {
			if spec.Name == "" {
				continue
			}
			if !validKeyName(spec.Name) {
				continue
			}
			m.Sync.Keys[spec.Name] = spec.Sensitive
		}
	} else {
		for _, name := range meta.AuthEnvVars {
			if !validKeyName(name) {
				continue
			}
			m.Sync.Keys[name] = true
		}
	}

	return m, nil
}
