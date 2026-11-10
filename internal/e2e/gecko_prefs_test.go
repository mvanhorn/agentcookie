package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeckoFixturePreferencesBeforeLaunch(t *testing.T) {
	profile := t.TempDir()
	if err := writeGeckoFixturePreferences(profile); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, "user.js")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("preferences must be private", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "user_pref(") {
			continue
		}
		pair := "[" + strings.TrimSuffix(strings.TrimPrefix(line, "user_pref("), ");") + "]"
		var values []any
		if err := json.Unmarshal([]byte(pair), &values); err != nil || len(values) != 2 {
			t.Fatalf("invalid preference: %s (%v)", line, err)
		}
		key, ok := values[0].(string)
		if !ok {
			t.Fatal("invalid preference key")
		}
		decoded[key] = values[1]
	}
	for _, key := range []string{
		"browser.migrate.automigrate.enabled", "browser.migrate.interactions.enabled", "browser.migrate.safari.enabled",
		"signon.rememberSignons", "signon.autofillForms", "signon.management.page.os-auth.enabled",
		"extensions.formautofill.creditCards.enabled", "extensions.formautofill.creditCards.os-auth.enabled",
		"security.osclientcerts.autoload", "security.enterprise_roots.enabled", "security.enterprise_roots.auto-enabled",
		"browser.shell.checkDefaultBrowser", "browser.aboutwelcome.enabled", "toolkit.telemetry.enabled",
		"datareporting.healthreport.uploadEnabled", "app.update.enabled", "app.update.auto",
		"network.captive-portal-service.enabled", "network.connectivity-service.enabled", "identity.fxaccounts.enabled",
	} {
		if value, ok := decoded[key]; !ok || value != false {
			t.Errorf("%s must be explicitly disabled, got %v", key, value)
		}
	}
	if decoded["browser.startup.homepage"] != "about:blank" || decoded["startup.homepage_welcome_url"] != "" {
		t.Fatal("first-run navigation enabled")
	}
	if decoded["network.trr.mode"] != float64(5) {
		t.Fatal("remote DNS should be disabled")
	}
	if err := writeGeckoFixturePreferences(profile); err == nil {
		t.Fatal("must not overwrite existing profile preferences")
	}
	after, _ := os.ReadFile(path)
	if string(raw) != string(after) {
		t.Fatal("existing preferences changed")
	}
}

func TestGeckoFixtureEnvironmentDisablesExternalNetwork(t *testing.T) {
	t.Setenv("MOZ_DISABLE_NONLOCAL_CONNECTIONS", "0")
	env := geckoFixtureEnvironment()
	last := map[string]string{}
	for _, variable := range env {
		if key, value, ok := strings.Cut(variable, "="); ok {
			last[key] = value
		}
	}
	for _, key := range []string{"MOZ_DISABLE_NONLOCAL_CONNECTIONS", "MOZ_CRASHREPORTER_DISABLE", "MOZ_CRASHREPORTER_NO_REPORT"} {
		if last[key] != "1" {
			t.Fatalf("%s not disabled", key)
		}
	}
}

func TestGeckoFixtureEnvironmentRejectsInheritedProfileOverrides(t *testing.T) {
	blocked := []string{"XRE_PROFILE_PATH", "XRE_PROFILE_LOCAL_PATH", "XRE_PROFILE_NAME", "XRE_RESTARTED_BY_PROFILE_MANAGER", "XRE_RESTARTED_BY_PROFILE_SELECTOR", "XRE_RESTART_TO_PROFILE_MANAGER", "MOZ_RESET_PROFILE_RESTART", "MOZ_RESET_PROFILE_MIGRATE_SESSION", "MOZ_LEGACY_PROFILES", "MOZ_PROFILE_PATH", "MOZ_RESTART", "MOZ_APP_RESTART", "MOZ_APP_SILENT_RESTART", "XRE_IMPORT_PROFILES"}
	input := []string{"HOME=/synthetic/fixture-home", "XDG_CONFIG_HOME=/synthetic/fixture-config", "DISPLAY=:synthetic", "UNRELATED_FIXTURE_KEY=preserved=value", "MOZ_DISABLE_NONLOCAL_CONNECTIONS=0", "MOZ_CRASHREPORTER_DISABLE=0", "MOZ_CRASHREPORTER_NO_REPORT=0"}
	for _, key := range blocked {
		input = append(input, key+"=/synthetic/unwanted-profile")
	}
	before := strings.Join(input, "\n")
	got := sanitizedGeckoFixtureEnvironment(input)
	values := map[string]string{}
	for _, entry := range got {
		key, value, _ := strings.Cut(entry, "=")
		if _, exists := values[key]; exists {
			t.Fatalf("duplicate environment key %s", key)
		}
		values[key] = value
	}
	for _, key := range blocked {
		if _, exists := values[key]; exists {
			t.Errorf("inherited profile override survived: %s", key)
		}
	}
	for key, want := range map[string]string{"HOME": "/synthetic/fixture-home", "XDG_CONFIG_HOME": "/synthetic/fixture-config", "DISPLAY": ":synthetic", "UNRELATED_FIXTURE_KEY": "preserved=value", "MOZ_DISABLE_NONLOCAL_CONNECTIONS": "1", "MOZ_CRASHREPORTER_DISABLE": "1", "MOZ_CRASHREPORTER_NO_REPORT": "1"} {
		if values[key] != want {
			t.Errorf("unexpected value for fixture key %s", key)
		}
	}
	if strings.Join(input, "\n") != before {
		t.Fatal("sanitizer mutated its input")
	}
}
