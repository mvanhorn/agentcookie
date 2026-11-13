// Package secretsbus reads the per-CLI files under ~/.agentcookie/secrets/
// into a wire-friendly payload, applies the manifest's sync policy, and
// exposes a watcher that fires on changes. The on-disk format is the public
// contract documented at docs/spec-agentcookie-secrets-bus-v1.md (v1).
//
// Source-side responsibilities live here. Sink-side write semantics live in
// the (forthcoming) writer in the same package.
package secretsbus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/mvanhorn/agentcookie/internal/envfile"

	"github.com/BurntSushi/toml"
)

// maxEnvFileBytes caps a single secrets.env at 256 KB per the v1 spec. Files
// larger than this are dropped on the source with a clear error rather than
// shipped, so a runaway log file accidentally renamed into the bus does not
// silently swamp the sink.
const maxEnvFileBytes = 256 * 1024

// Payload is the on-wire shape carried inside protocol.SyncEnvelope. One
// entry per registered CLI, only including CLIs whose effective sync policy
// resolves to "ship something." The map values are post-filter (per-key
// sync=false entries already removed).
type Payload struct {
	// CLIs maps cli-name -> env map. Empty map means "no CLIs registered yet."
	CLIs map[string]map[string]string `json:"clis"`
}

// IsEmpty reports whether the payload carries any CLI data.
func (p *Payload) IsEmpty() bool {
	return p == nil || len(p.CLIs) == 0
}

// Manifest mirrors the v1 manifest.toml shape. Per the spec, [sync.keys]
// stays source-side and does NOT travel to the sink. Filtering happens here.
type Manifest struct {
	SchemaVersion int    `toml:"schema_version"`
	DisplayName   string `toml:"display_name"`
	Sync          struct {
		Default bool            `toml:"default"`
		Keys    map[string]bool `toml:"keys"`
	} `toml:"sync"`
}

// defaultSync returns the effective default. v1 spec: default is true when
// the manifest omits [sync] entirely or omits sync.default.
//
//nolint:unused // intentionally retained; documents the v1 sync default semantics
func (m *Manifest) defaultSync() bool {
	// Burntsushi/toml zero-values Default to false; we need to distinguish
	// "omitted" from "explicit false." We can't from the parsed struct
	// alone, so callers parse via parseManifest which sets a sentinel.
	return m.Sync.Default
}

// SecretsRoot returns the absolute path of the secrets root directory.
// Caller passes a homeDir to keep the function testable without HOME magic.
func SecretsRoot(homeDir string) string {
	return filepath.Join(homeDir, ".agentcookie", "secrets")
}

// LoadPayload walks the secrets root, reads each per-CLI dir's env file and
// manifest, applies per-key sync filtering, and returns the payload. The
// caller (source push code) packs this into the wire envelope.
//
// Behavior on edge cases:
//   - Missing root: returns empty payload, no error.
//   - Per-CLI dir with no secrets.env (manifest-only): skipped, logged.
//   - secrets.env exceeding maxEnvFileBytes: skipped, returns the error
//     for the caller to log, BUT still returns whatever was loaded for
//     other CLIs.
//   - Manifest parse error: skipped (the per-CLI dir is excluded) and
//     returned as a non-fatal error.
//   - Unexpected file at root level (not a directory): skipped silently.
func LoadPayload(homeDir string) (*Payload, []error) {
	root := SecretsRoot(homeDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return &Payload{CLIs: map[string]map[string]string{}}, nil
		}
		return nil, []error{fmt.Errorf("read secrets root %s: %w", root, err)}
	}

	out := &Payload{CLIs: map[string]map[string]string{}}
	var nonFatal []error

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cliName := e.Name()
		if !validCLIName(cliName) {
			nonFatal = append(nonFatal, fmt.Errorf("skipping invalid cli-name directory %q (must match v1 spec naming)", cliName))
			continue
		}
		cliDir := filepath.Join(root, cliName)
		envPath := filepath.Join(cliDir, "secrets.env")
		manifestPath := filepath.Join(cliDir, "manifest.toml")

		envInfo, err := os.Stat(envPath)
		if err != nil {
			if !os.IsNotExist(err) {
				nonFatal = append(nonFatal, fmt.Errorf("%s: stat secrets.env: %w", cliName, err))
			}
			continue
		}
		if envInfo.Size() > maxEnvFileBytes {
			nonFatal = append(nonFatal, fmt.Errorf("%s: secrets.env is %d bytes, over the %d byte limit; not shipping this CLI", cliName, envInfo.Size(), maxEnvFileBytes))
			continue
		}

		envMap, err := envfile.ReadFile(envPath)
		if err != nil {
			nonFatal = append(nonFatal, fmt.Errorf("%s: parse secrets.env: %w", cliName, err))
			continue
		}

		manifest, manifestExplicitDefault, mErr := loadManifest(manifestPath)
		if mErr != nil {
			nonFatal = append(nonFatal, fmt.Errorf("%s: parse manifest.toml: %w", cliName, mErr))
			continue
		}

		filtered := applySyncPolicy(envMap, manifest, manifestExplicitDefault)
		if len(filtered) > 0 {
			out.CLIs[cliName] = filtered
		}
	}

	return out, nonFatal
}

// validCLIName mirrors the v1 spec: lowercase, alphanumeric + hyphens, no
// dots, no slashes, no ".." traversal. Hyphens may not lead or trail.
func validCLIName(name string) bool {
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

// loadManifest reads manifest.toml if present. Returns the parsed manifest,
// a boolean indicating whether sync.default was explicitly set in the file
// (vs absent and zero-valued), and any parse error.
//
// When the file is missing the function returns a manifest with default
// sync=true (v1 spec default), explicitDefault=false, no error.
func loadManifest(path string) (*Manifest, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			m := &Manifest{}
			m.Sync.Default = true
			return m, false, nil
		}
		return nil, false, err
	}
	// Two-pass: first decode into a generic map to detect whether
	// sync.default was set explicitly; second pass into the typed struct.
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, false, fmt.Errorf("unmarshal: %w", err)
	}
	explicit := false
	if syncRaw, ok := raw["sync"].(map[string]any); ok {
		if _, ok := syncRaw["default"]; ok {
			explicit = true
		}
	}

	m := &Manifest{}
	if err := toml.Unmarshal(data, m); err != nil {
		return nil, false, fmt.Errorf("typed unmarshal: %w", err)
	}
	if !explicit {
		// Apply v1 default of true.
		m.Sync.Default = true
	}
	return m, explicit, nil
}

// applySyncPolicy returns the subset of envMap that should ship to the sink
// based on the manifest's [sync] default + [sync.keys] per-key overrides.
// Per the v1 spec, the [sync.keys] table itself does NOT travel to the
// sink; only filtered data does.
func applySyncPolicy(envMap map[string]string, m *Manifest, explicitDefault bool) map[string]string {
	out := map[string]string{}
	defaultSync := true
	if explicitDefault {
		defaultSync = m.Sync.Default
	}
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic ordering for tests + wire stability
	for _, k := range keys {
		shouldSync := defaultSync
		if override, ok := m.Sync.Keys[k]; ok {
			shouldSync = override
		}
		if shouldSync {
			out[k] = envMap[k]
		}
	}
	return out
}
