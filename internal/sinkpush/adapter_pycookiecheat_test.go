package sinkpush

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// newTestAdapter builds a PycookiecheatStyleAdapter pointing at a temp
// config dir with the binary placed inside the temp dir so IsInstalled
// returns true. Returns the adapter and the temp dir for assertions.
func newTestAdapter(t *testing.T, name, hostPattern, baseURL string) (*PycookiecheatStyleAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	a := &PycookiecheatStyleAdapter{
		name:        name,
		binary:      bin,
		hostPattern: hostPattern,
		configDir:   filepath.Join(dir, "config"),
		baseURL:     baseURL,
	}
	return a, dir
}

func TestPycookiecheatAdapter_Identity(t *testing.T) {
	for _, tc := range []struct {
		make    func() *PycookiecheatStyleAdapter
		name    string
		host    string
		baseURL string
	}{
		{NewAirbnb, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com"},
		{NewEbay, "ebay-pp-cli", "%ebay%", "https://www.ebay.com"},
		{NewPagliacci, "pagliacci-pp-cli", "%pagliacci%", "https://pagliacci.com"},
	} {
		a := tc.make()
		if a.Name() != tc.name {
			t.Errorf("Name: got %q, want %q", a.Name(), tc.name)
		}
		if got := a.CookieHostPatterns(); len(got) != 1 || got[0] != tc.host {
			t.Errorf("HostPatterns for %s: got %v, want [%s]", tc.name, got, tc.host)
		}
		if a.baseURL != tc.baseURL {
			t.Errorf("baseURL for %s: got %q, want %q", tc.name, a.baseURL, tc.baseURL)
		}
	}
}

func TestPycookiecheatAdapter_PushCreatesFreshConfig(t *testing.T) {
	a, _ := newTestAdapter(t, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com")
	cookies := []chrome.Cookie{
		{HostKey: ".airbnb.com", Name: "_session", Value: "sess123"},
		{HostKey: ".airbnb.com", Name: "csrf", Value: "abc"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// config.toml exists with the right shape
	configBytes, err := os.ReadFile(a.configTOMLPath())
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	configStr := string(configBytes)
	for _, want := range []string{
		"base_url = 'https://www.airbnb.com'",
		"access_token = '_session=sess123; csrf=abc'",
		"auth_header = ''",
		"refresh_token = ''",
	} {
		if !strings.Contains(configStr, want) {
			t.Errorf("config.toml missing %q. Full:\n%s", want, configStr)
		}
	}

	// cookies.json exists with the same Cookie-header value
	cookiesJSON, err := os.ReadFile(a.cookiesJSONPath())
	if err != nil {
		t.Fatalf("read cookies.json: %v", err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(cookiesJSON, &parsed); err != nil {
		t.Fatalf("parse cookies.json: %v", err)
	}
	if parsed["cookies"] != "_session=sess123; csrf=abc" {
		t.Errorf("cookies.json mismatch: got %q", parsed["cookies"])
	}

	// File modes are 0600
	for _, p := range []string{a.configTOMLPath(), a.cookiesJSONPath()} {
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode: got %o, want 0600", p, info.Mode().Perm())
		}
	}
}

func TestPycookiecheatAdapter_PushPreservesExistingConfig(t *testing.T) {
	a, _ := newTestAdapter(t, "ebay-pp-cli", "%ebay%", "https://www.ebay.com")
	if err := os.MkdirAll(a.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"base_url = 'https://custom.ebay.com'", // user customized
		"auth_header = 'X-Custom: yes'",        // user customized
		"access_token = 'OLD_VALUE'",
		"refresh_token = 'preserved'",
		"token_expiry = 2026-12-31T00:00:00Z",
		"",
	}, "\n")
	if err := os.WriteFile(a.configTOMLPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Push([]chrome.Cookie{
		{HostKey: ".ebay.com", Name: "new", Value: "fresh"},
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	final, _ := os.ReadFile(a.configTOMLPath())
	finalStr := string(final)
	for _, want := range []string{
		"base_url = 'https://custom.ebay.com'", // preserved
		"auth_header = 'X-Custom: yes'",        // preserved
		"access_token = 'new=fresh'",           // updated
		"refresh_token = 'preserved'",          // preserved
		"token_expiry = 2026-12-31T00:00:00Z",  // preserved
	} {
		if !strings.Contains(finalStr, want) {
			t.Errorf("missing or changed: %q. Full:\n%s", want, finalStr)
		}
	}
	if strings.Contains(finalStr, "OLD_VALUE") {
		t.Errorf("old access_token still present after update")
	}
}

func TestPycookiecheatAdapter_PushFiltersEmptyValueCookies(t *testing.T) {
	a, _ := newTestAdapter(t, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com")
	cookies := []chrome.Cookie{
		{HostKey: ".airbnb.com", Name: "_session", Value: "real"},
		{HostKey: ".airbnb.com", Name: "consent_flag", Value: ""}, // dropped
		{HostKey: ".airbnb.com", Name: "csrf", Value: "abc"},
	}
	if err := a.Push(cookies); err != nil {
		t.Fatalf("Push: %v", err)
	}
	configStr, _ := os.ReadFile(a.configTOMLPath())
	if strings.Contains(string(configStr), "consent_flag") {
		t.Errorf("empty-value cookie should have been dropped")
	}
	if !strings.Contains(string(configStr), "_session=real") {
		t.Errorf("real cookie missing")
	}
}

func TestPycookiecheatAdapter_PushNoCookies_NoOp(t *testing.T) {
	a, _ := newTestAdapter(t, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com")
	if err := a.Push([]chrome.Cookie{
		{HostKey: ".airbnb.com", Name: "n", Value: ""}, // all empty
	}); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// No config dir should have been created since we never had a
	// non-empty Cookie header to write.
	if _, err := os.Stat(a.configTOMLPath()); err == nil {
		t.Errorf("config.toml should not exist when all cookies have empty values")
	}
}

func TestPycookiecheatAdapter_AtomicWrite(t *testing.T) {
	a, _ := newTestAdapter(t, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com")
	_ = a.Push([]chrome.Cookie{{HostKey: ".airbnb.com", Name: "n", Value: "v"}})
	// After Push, no .tmp file should remain.
	for _, suffix := range []string{".agentcookie.tmp"} {
		if _, err := os.Stat(a.configTOMLPath() + suffix); err == nil {
			t.Errorf("leftover %s after Push", suffix)
		}
		if _, err := os.Stat(a.cookiesJSONPath() + suffix); err == nil {
			t.Errorf("leftover %s after cookies.json Push", suffix)
		}
	}
}

func TestPycookiecheatAdapter_HostFilterIsolation(t *testing.T) {
	// Each adapter's CookieHostPatterns should be its CLI's domain
	// only -- no leakage from other adapters' domains.
	for _, tc := range []struct {
		make    func() *PycookiecheatStyleAdapter
		match   string
		nomatch string
	}{
		{NewAirbnb, ".airbnb.com", ".ebay.com"},
		{NewEbay, ".ebay.com", ".airbnb.com"},
		{NewPagliacci, ".pagliacci.com", ".airbnb.com"},
	} {
		a := tc.make()
		patterns := a.CookieHostPatterns()
		if !matchLike(tc.match, patterns[0]) {
			t.Errorf("%s adapter pattern %q should match %q", a.Name(), patterns[0], tc.match)
		}
		if matchLike(tc.nomatch, patterns[0]) {
			t.Errorf("%s adapter pattern %q should NOT match %q (cross-domain leak)", a.Name(), patterns[0], tc.nomatch)
		}
	}
}

func TestReplaceAccessToken_PatchesExistingLine(t *testing.T) {
	existing := "base_url = 'x'\naccess_token = 'OLD'\nrefresh_token = ''\n"
	got := replaceAccessToken(existing, "NEW")
	want := "base_url = 'x'\naccess_token = 'NEW'\nrefresh_token = ''\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReplaceAccessToken_InsertsWhenMissing(t *testing.T) {
	// Defensive: if the existing config has no access_token line
	// (drifted format), prepend a new one rather than silently
	// dropping the update.
	existing := "base_url = 'x'\nrefresh_token = ''\n"
	got := replaceAccessToken(existing, "NEW")
	if !strings.HasPrefix(got, "access_token = 'NEW'\n") {
		t.Errorf("expected new access_token at top; got:\n%s", got)
	}
	if !strings.Contains(got, "base_url = 'x'") {
		t.Errorf("existing content lost; got:\n%s", got)
	}
}

func TestEscapeTOMLSingleQuoted_StripsEmbeddedSingleQuotes(t *testing.T) {
	if got := escapeTOMLSingleQuoted("normal value"); got != "normal value" {
		t.Errorf("clean string changed: %q", got)
	}
	if got := escapeTOMLSingleQuoted("has'quote"); got != "hasquote" {
		t.Errorf("single-quoted input not handled: %q", got)
	}
}

func TestPycookiecheatAdapter_PushIsIdempotent(t *testing.T) {
	a, _ := newTestAdapter(t, "airbnb-pp-cli", "%airbnb%", "https://www.airbnb.com")
	cookies := []chrome.Cookie{{HostKey: ".airbnb.com", Name: "x", Value: "1"}}
	if err := a.Push(cookies); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(a.configTOMLPath())
	if err := a.Push(cookies); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(a.configTOMLPath())
	if string(first) != string(second) {
		t.Errorf("second push changed config.toml content (expected identical)")
	}
}
