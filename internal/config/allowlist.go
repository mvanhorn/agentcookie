package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type CookiePolicy string

const (
	CookiePolicyBlocklist CookiePolicy = "blocklist"
	CookiePolicyAllowlist CookiePolicy = "allowlist"
)

// BlocklistEntry is one explicitly opted-OUT cookie domain. Pattern follows
// SQLite LIKE syntax (use '%' as wildcard, e.g. "%.chase.com"). Cookies whose
// host_key matches any pattern are NOT synced in blocklist mode. In allowlist
// mode, entries are explicitly opted-IN domains.
type BlocklistEntry struct {
	Pattern     string `yaml:"pattern" json:"pattern"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Blocklist is the on-disk shape of blocklist.yaml. Version is bumped if/when
// the file format changes incompatibly. Omitted policy preserves legacy
// blocklist semantics. Empty domains list (or missing file) means sync all
// cookies only in blocklist mode; empty allowlist mode syncs no cookies.
type Blocklist struct {
	Version int              `yaml:"version" json:"version"`
	Policy  CookiePolicy     `yaml:"policy,omitempty" json:"policy,omitempty"`
	Domains []BlocklistEntry `yaml:"domains" json:"domains"`
}

// LoadBlocklist reads blocklist.yaml from dir. A missing file is NOT an error;
// it produces an empty Blocklist (sync-all semantic). v0.3 inverts the v0.2
// allowlist model; if a legacy allowlist.yaml is present and blocklist.yaml is
// not, the legacy file is renamed to allowlist.yaml.v2.bak and an empty
// blocklist is returned with a one-line warning to stderr.
func LoadBlocklist(dir string) (*Blocklist, error) {
	blocklistPath := filepath.Join(dir, "blocklist.yaml")
	if !fileExists(blocklistPath) {
		legacy := filepath.Join(dir, "allowlist.yaml")
		if fileExists(legacy) {
			migrateLegacyAllowlist(legacy)
		}
		return &Blocklist{Version: 1}, nil
	}
	var bl Blocklist
	if err := loadYAML(blocklistPath, &bl); err != nil {
		return nil, err
	}
	if bl.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported blocklist version %d (this binary speaks version 1)", blocklistPath, bl.Version)
	}
	switch bl.PolicyMode() {
	case CookiePolicyBlocklist, CookiePolicyAllowlist:
	default:
		return nil, fmt.Errorf("%s: policy must be %q or %q (got %q)", blocklistPath, CookiePolicyBlocklist, CookiePolicyAllowlist, bl.Policy)
	}
	for i, e := range bl.Domains {
		if e.Pattern == "" {
			return nil, fmt.Errorf("%s: domains[%d].pattern is empty", blocklistPath, i)
		}
	}
	return &bl, nil
}

// PolicyMode returns the effective cookie filter policy. Empty is the legacy
// blocklist mode so old blocklist.yaml files keep their behavior unchanged.
func (bl *Blocklist) PolicyMode() CookiePolicy {
	if bl == nil || bl.Policy == "" {
		return CookiePolicyBlocklist
	}
	return bl.Policy
}

// CookiePolicySummary returns the operator-facing policy label.
func (bl *Blocklist) CookiePolicySummary() string {
	if bl == nil {
		return "sync-all"
	}
	if bl.PolicyMode() == CookiePolicyAllowlist {
		return string(CookiePolicyAllowlist)
	}
	if len(bl.Domains) == 0 {
		return "sync-all"
	}
	return string(CookiePolicyBlocklist)
}

// LoadAllowlist is a compatibility wrapper kept so existing callers still
// compile during the v0.2 -> v0.3 transition. Behavior is identical to
// LoadBlocklist; the returned struct uses the new shape.
func LoadAllowlist(dir string) (*Blocklist, error) {
	return LoadBlocklist(dir)
}

// migrateLegacyAllowlist renames allowlist.yaml to allowlist.yaml.v2.bak
// when blocklist.yaml is not present. v0.3 changes the filter default from
// opt-in to opt-out; preserving the old file as .bak makes the change
// recoverable but does not silently keep the old semantics.
func migrateLegacyAllowlist(path string) {
	backup := path + ".v2.bak"
	if fileExists(backup) {
		return
	}
	if err := os.Rename(path, backup); err == nil {
		fmt.Fprintf(os.Stderr, "agentcookie: migrated %s -> %s (v0.3 inverts the filter to opt-out; sync-all is now the default)\n", path, backup)
	}
}

// fileExists is a tiny helper duplicated here to avoid importing internal/cli.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// AllowlistEntry is retained as a type alias so callers that named the v0.2
// type continue to compile. New code should use BlocklistEntry directly.
type AllowlistEntry = BlocklistEntry

// Allowlist is retained as a type alias for the v0.2 -> v0.3 transition. New
// code should use Blocklist.
type Allowlist = Blocklist
