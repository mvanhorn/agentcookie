package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// These preferences belong exclusively to a newly-created throwaway fixture.
// They never alter a user's browser or product launcher settings. Forks may
// ignore preferences they do not implement; this is not an OS security boundary.
func geckoFixturePreferences() map[string]any {
	return map[string]any{
		// Do not offer or automatically import another browser's profiles/secrets.
		"browser.migrate.automigrate.enabled":                         false,
		"browser.migrate.interactions.enabled":                        false,
		"browser.migrate.chrome.enabled":                              false,
		"browser.migrate.chromium.enabled":                            false,
		"browser.migrate.edge.enabled":                                false,
		"browser.migrate.safari.enabled":                              false,
		"browser.startup.firstrunSkipsHomepage":                       true,
		"browser.startup.homepage":                                    "about:blank",
		"browser.startup.page":                                        0,
		"startup.homepage_welcome_url":                                "",
		"startup.homepage_welcome_url.additional":                     "",
		"startup.homepage_override_url":                               "",
		"browser.shell.checkDefaultBrowser":                           false,
		"browser.aboutwelcome.enabled":                                false,
		"browser.newtabpage.enabled":                                  false,
		"browser.newtabpage.activity-stream.feeds.telemetry":          false,
		"browser.newtabpage.activity-stream.telemetry":                false,
		"browser.newtabpage.activity-stream.feeds.snippets":           false,
		"browser.newtabpage.activity-stream.feeds.section.topstories": false,
		// No fixture credential saving, autofill, OS authentication or client certs.
		"signon.rememberSignons":                                false,
		"signon.autofillForms":                                  false,
		"signon.generation.enabled":                             false,
		"signon.management.page.os-auth.enabled":                false,
		"extensions.formautofill.addresses.enabled":             false,
		"extensions.formautofill.creditCards.enabled":           false,
		"extensions.formautofill.creditCards.os-auth.enabled":   false,
		"security.osclientcerts.autoload":                       false,
		"security.enterprise_roots.enabled":                     false,
		"security.enterprise_roots.auto-enabled":                false,
		"security.certerrors.mitm.auto_enable_enterprise_roots": false,
		// Avoid first-run/background network work. Test pages use loopback HTTP only.
		"app.update.enabled":                           false,
		"app.update.auto":                              false,
		"app.update.url":                               "http://127.0.0.1:9/disabled",
		"app.normandy.enabled":                         false,
		"app.shield.optoutstudies.enabled":             false,
		"extensions.update.enabled":                    false,
		"extensions.getAddons.cache.enabled":           false,
		"extensions.getAddons.showPane":                false,
		"extensions.blocklist.enabled":                 false,
		"extensions.systemAddon.update.enabled":        false,
		"extensions.systemAddon.update.url":            "",
		"datareporting.healthreport.uploadEnabled":     false,
		"datareporting.policy.dataSubmissionEnabled":   false,
		"toolkit.telemetry.enabled":                    false,
		"toolkit.telemetry.unified":                    false,
		"toolkit.telemetry.server":                     "http://127.0.0.1:9/disabled",
		"toolkit.telemetry.shutdownPingSender.enabled": false,
		"browser.discovery.enabled":                    false,
		"browser.search.update":                        false,
		"browser.search.suggest.enabled":               false,
		"browser.safebrowsing.malware.enabled":         false,
		"browser.safebrowsing.phishing.enabled":        false,
		"browser.safebrowsing.downloads.enabled":       false,
		"services.settings.server":                     "http://127.0.0.1:9/disabled",
		"services.sync.enabled":                        false,
		"identity.fxaccounts.enabled":                  false,
		"network.captive-portal-service.enabled":       false,
		"network.connectivity-service.enabled":         false,
		"network.dns.disablePrefetch":                  true,
		"network.prefetch-next":                        false,
		"network.predictor.enabled":                    false,
		"network.http.speculative-parallel-limit":      0,
		"network.trr.mode":                             5,
		"media.gmp-manager.updateEnabled":              false,
		"media.gmp-provider.enabled":                   false,
		"media.gmp-widevinecdm.enabled":                false,
		"media.peerconnection.enabled":                 false,
		"dom.push.enabled":                             false,
		"geo.enabled":                                  false,
	}
}

func writeGeckoFixturePreferences(profile string) error {
	prefs := geckoFixturePreferences()
	keys := make([]string, 0, len(prefs))
	for key := range prefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	body.WriteString("// agentcookie throwaway test fixture only. Never copy to a real profile.\n")
	for _, key := range keys {
		name, _ := json.Marshal(key)
		value, err := json.Marshal(prefs[key])
		if err != nil {
			return err
		}
		fmt.Fprintf(&body, "user_pref(%s, %s);\n", name, value)
	}
	// Exclusive create ensures an accidental call with an existing profile cannot
	// overwrite its preferences. NewGecko only passes its freshly-created dir.
	file, err := os.OpenFile(filepath.Join(profile, "user.js"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(body.String())
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// Firefox's test-only network guard is defense in depth, not a substitute for
// observing the final runner's actual native activity. A fork may ignore it.
func geckoFixtureEnvironment() []string {
	return sanitizedGeckoFixtureEnvironment(os.Environ())
}

// Firefox consults restart profile paths before the explicit -profile flag.
// Filter these inherited controls by key only; never inspect or log their
// values. HOME/XDG_CONFIG_HOME already point to the newly created fixture.
func sanitizedGeckoFixtureEnvironment(inherited []string) []string {
	env := make([]string, 0, len(inherited)+3)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "XRE_PROFILE") || strings.HasPrefix(key, "XRE_RESTART") || strings.HasPrefix(key, "XRE_RESET_PROFILE") || strings.HasPrefix(key, "MOZ_PROFILE") || strings.HasPrefix(key, "MOZ_RESET_PROFILE") || strings.HasPrefix(key, "MOZ_RESTART") {
			continue
		}
		switch key {
		case "MOZ_LEGACY_PROFILES", "MOZ_APP_RESTART", "MOZ_APP_SILENT_RESTART", "XRE_IMPORT_PROFILES",
			"MOZ_DISABLE_NONLOCAL_CONNECTIONS", "MOZ_CRASHREPORTER_DISABLE", "MOZ_CRASHREPORTER_NO_REPORT":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "MOZ_DISABLE_NONLOCAL_CONNECTIONS=1", "MOZ_CRASHREPORTER_DISABLE=1", "MOZ_CRASHREPORTER_NO_REPORT=1")
}
