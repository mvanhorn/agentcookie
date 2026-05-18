package sinkpush

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// PycookiecheatStyleAdapter pushes cookies into PP CLIs whose auth path
// is "pycookiecheat-style": a TOML config + (optionally) a cookies.json
// shadow file, both carrying the same Cookie-header value in a single
// field.
//
// Discovered on 2026-05-17 from inspecting Matt's MBP after airbnb-pp-cli
// and ebay-pp-cli auth flows ran successfully:
//
//   ~/.config/<cli>/config.toml   — TOML; access_token = '<cookie header>'
//   ~/.config/<cli>/cookies.json  — JSON; {"cookies": "<cookie header>"}
//
// At least three PP CLIs share this exact format (airbnb-pp-cli,
// ebay-pp-cli, pagliacci-pp-cli) -- they each shell out to
// pycookiecheat or one of its alternatives, capture the Cookie
// header, and write it into both files (airbnb writes both; ebay
// only writes config.toml). This adapter reproduces that write
// directly, skipping the pycookiecheat invocation entirely.
//
// Concrete adapters wrap this struct via NewAirbnb, NewEbay,
// NewPagliacci. Each fills in: CLI name, host pattern, config-dir
// basename, base_url default for fresh installs.
type PycookiecheatStyleAdapter struct {
	name        string // e.g. "airbnb-pp-cli"
	binary      string // resolved absolute path
	hostPattern string // single LIKE pattern
	configDir   string // resolved absolute path to ~/.config/<cli>/
	baseURL     string // default base_url for fresh config.toml
}

// newPycookiecheatStyleAdapter is the internal constructor. Concrete
// adapter constructors (NewAirbnb, etc.) call this with their per-CLI
// values; tests can construct the struct directly to point at a temp
// dir.
func newPycookiecheatStyleAdapter(name, hostPattern, configBasename, baseURL string) *PycookiecheatStyleAdapter {
	home, _ := os.UserHomeDir()
	return &PycookiecheatStyleAdapter{
		name:        name,
		binary:      filepath.Join(home, "go", "bin", name),
		hostPattern: hostPattern,
		configDir:   filepath.Join(home, ".config", configBasename),
		baseURL:     baseURL,
	}
}

func (a *PycookiecheatStyleAdapter) Name() string { return a.name }

func (a *PycookiecheatStyleAdapter) CLIBinary() string { return a.binary }

func (a *PycookiecheatStyleAdapter) IsInstalled() bool {
	info, err := os.Stat(a.binary)
	return err == nil && !info.IsDir()
}

func (a *PycookiecheatStyleAdapter) CookieHostPatterns() []string {
	return []string{a.hostPattern}
}

// Push writes the cookies into the CLI's config.toml access_token field
// and a matching cookies.json. Atomic writes via temp + rename. If
// config.toml already exists, only the access_token line is rewritten
// (preserves user-customized base_url and any other fields).
//
// v0.12: when the agentcookie master key Keychain item is present, the
// header value is sealed (SealedPrefix + base64) before being written
// to either file. PP CLIs that consume these files detect the prefix
// and unseal transparently. Sinks without the master key fall back to
// plaintext (v0.11 shape) so partial installs still work.
func (a *PycookiecheatStyleAdapter) Push(cookies []chrome.Cookie) error {
	header := formatCookieHeader(cookies)
	if header == "" {
		return nil
	}
	onDiskHeader, err := maybeSeal(header)
	if err != nil {
		return fmt.Errorf("seal header: %w", err)
	}
	if err := os.MkdirAll(a.configDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", a.configDir, err)
	}
	if err := a.writeConfigTOML(onDiskHeader); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	if err := a.writeCookiesJSON(onDiskHeader); err != nil {
		return fmt.Errorf("write cookies.json: %w", err)
	}
	return nil
}

// configTOMLPath returns the adapter's expected config.toml path.
func (a *PycookiecheatStyleAdapter) configTOMLPath() string {
	return filepath.Join(a.configDir, "config.toml")
}

// cookiesJSONPath returns the adapter's expected cookies.json path.
func (a *PycookiecheatStyleAdapter) cookiesJSONPath() string {
	return filepath.Join(a.configDir, "cookies.json")
}

// writeConfigTOML updates an existing config.toml's access_token line
// in place, preserving any other user-set fields. If config.toml does
// not exist, writes a fresh one from a template populated with the
// adapter's baseURL.
func (a *PycookiecheatStyleAdapter) writeConfigTOML(header string) error {
	path := a.configTOMLPath()
	existing, err := os.ReadFile(path)
	var content string
	if err == nil {
		// File exists -- patch the access_token line, leave the rest alone.
		content = replaceAccessToken(string(existing), header)
	} else if os.IsNotExist(err) {
		// Fresh install -- write the canonical template.
		content = freshConfigTOML(a.baseURL, header)
	} else {
		return fmt.Errorf("read existing: %w", err)
	}
	return atomicWriteFile(path, []byte(content), 0o600)
}

// writeCookiesJSON writes the cookies.json file. The schema is a single
// "cookies" field carrying the Cookie-header value -- the exact shape
// airbnb-pp-cli writes after a successful auth login --chrome.
func (a *PycookiecheatStyleAdapter) writeCookiesJSON(header string) error {
	body, err := json.MarshalIndent(map[string]string{"cookies": header}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return atomicWriteFile(a.cookiesJSONPath(), body, 0o600)
}

// accessTokenLine matches the existing `access_token = '...'` line so
// the patch path preserves whitespace and surrounding fields. Single-
// quoted form per TOML literal-string convention; matches the format
// the PP CLIs themselves write.
var accessTokenLine = regexp.MustCompile(`(?m)^access_token\s*=\s*'[^']*'\s*$`)

// replaceAccessToken substitutes the access_token line with the new
// cookie header value. If no access_token line exists in the file,
// the value is inserted as a new line at the top of the file
// (defensive: never silently dropping the auth update).
func replaceAccessToken(existing, header string) string {
	newLine := "access_token = '" + escapeTOMLSingleQuoted(header) + "'"
	if accessTokenLine.MatchString(existing) {
		return accessTokenLine.ReplaceAllString(existing, newLine)
	}
	// Defensive fallback: prepend a new line. Avoids silent loss of
	// the auth update when the CLI's config format drifts.
	if strings.HasSuffix(existing, "\n") {
		return newLine + "\n" + existing
	}
	return newLine + "\n" + existing + "\n"
}

// escapeTOMLSingleQuoted handles the rare cookie-value-contains-single-
// quote case. TOML single-quoted strings are literal; escaping means
// switching to a double-quoted string, but that requires escaping
// backslashes and quotes inside the value. Simpler safe approach:
// strip embedded single quotes (extremely rare in cookie values --
// Chrome never emits them, but defensive against pathological input).
func escapeTOMLSingleQuoted(s string) string {
	if !strings.ContainsRune(s, '\'') {
		return s
	}
	return strings.ReplaceAll(s, "'", "")
}

// freshConfigTOML returns the canonical config.toml content for a CLI
// whose config dir is being created for the first time by this
// adapter. Matches the layout the PP CLIs themselves write after a
// successful auth login --chrome -- so the CLI sees a familiar shape
// and the user can hand-edit fields like base_url without conflicting
// with future adapter pushes (which only touch the access_token line).
func freshConfigTOML(baseURL, header string) string {
	return strings.Join([]string{
		"base_url = '" + baseURL + "'",
		"auth_header = ''",
		"access_token = '" + escapeTOMLSingleQuoted(header) + "'",
		"refresh_token = ''",
		"token_expiry = 0001-01-01T00:00:00Z",
		"client_id = ''",
		"client_secret = ''",
		"",
	}, "\n")
}

// atomicWriteFile writes content to path via a temp-file + rename so
// readers either see the prior state or the new state, never a
// half-written intermediate. mode is applied to the final file.
func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	tmp := path + ".agentcookie.tmp"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
