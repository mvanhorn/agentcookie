package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/chromepaths"
	"github.com/mvanhorn/agentcookie/internal/config"
)

const claudeAccountsCheckName = "Claude in Chrome accounts"

// claudeAccountsEnv is what the Claude in Chrome account check reads, so
// tests never touch a real Chrome profile or the Keychain.
type claudeAccountsEnv struct {
	// accounts lists each profile's Claude in Chrome sign-in.
	accounts func() ([]chrome.ClaudeExtAccount, []error)
	// cookieReadAllowed is false when skip_chrome_sqlite forbids reading
	// Chrome Safe Storage; cookieOrg is then never called.
	cookieReadAllowed bool
	// cookieOrg returns the org of the profile's claude.ai web session (its
	// lastActiveOrg cookie), or "" when the profile is not signed in.
	cookieOrg func(profile string) (string, error)
	// labels maps a profile folder to its Google account or display name.
	labels func() map[string]string
}

// checkClaudeAccounts compares, in each Chrome profile, the Claude account
// Claude in Chrome is signed into with the profile's claude.ai login. When
// they differ the extension shows "Claude in Chrome uses a different
// account" and stops working for that profile.
func checkClaudeAccounts(sinkCfg *config.SinkConfig) Check {
	root := chromepaths.ChromeProfileRoot()
	// One Keychain read for the whole check, however many profiles.
	decryptKey := sync.OnceValues(func() ([]byte, error) { return getChromeDecryptKey("chrome") })
	return checkClaudeAccountsWith(claudeAccountsEnv{
		accounts:          func() ([]chrome.ClaudeExtAccount, []error) { return chrome.ClaudeProfileAccounts(root) },
		cookieReadAllowed: safeStorageReadAllowed(sinkCfg),
		cookieOrg: func(profile string) (string, error) {
			return claudeCookieOrg(root, profile, decryptKey)
		},
		labels: func() map[string]string { return chromeProfileLabels(root) },
	})
}

func checkClaudeAccountsWith(e claudeAccountsEnv) Check {
	accounts, errs := e.accounts()
	if len(accounts) == 0 {
		detail := "Claude in Chrome is not signed in in any Chrome profile"
		if len(errs) > 0 {
			detail = "could not read Claude in Chrome's account: " + errors.Join(errs...).Error()
		}
		return Check{Name: claudeAccountsCheckName, Severity: SeveritySkipped, Detail: detail}
	}
	labels := e.labels()
	label := func(profile string) string {
		if l := labels[profile]; l != "" {
			return profile + " (" + l + ")"
		}
		return profile
	}

	if !e.cookieReadAllowed {
		names := make([]string, 0, len(accounts))
		for _, a := range accounts {
			names = append(names, label(a.Profile))
		}
		return Check{
			Name:     claudeAccountsCheckName,
			Severity: SeverityOK,
			Detail:   "signed in: " + strings.Join(names, ", ") + "; claude.ai logins not compared because skip_chrome_sqlite is set",
		}
	}

	// Which profile's extension holds each org, for naming a cross match.
	extOrgAccount := map[string]chrome.ClaudeExtAccount{}
	for _, a := range accounts {
		if a.TokenOrg != "" {
			extOrgAccount[a.TokenOrg] = a
		}
	}

	var ok, mismatched, skipped []string
	// A store that failed to read may be the profile on the wrong account,
	// so it is named rather than dropped. Errors already carry the profile.
	for _, err := range errs {
		skipped = append(skipped, err.Error())
	}
	for _, a := range accounts {
		name := label(a.Profile)
		if a.TokenOrg == "" {
			skipped = append(skipped, name+": Claude in Chrome has not stored its org")
			continue
		}
		org, err := e.cookieOrg(a.Profile)
		switch {
		case err != nil:
			skipped = append(skipped, name+": "+err.Error())
		case org == "":
			skipped = append(skipped, name+": not signed in to claude.ai")
		case org == a.TokenOrg:
			ok = append(ok, name)
		default:
			// Confirmed only when another profile's extension holds that org
			// under a different account; the same account in another org is
			// not proof.
			if other, found := extOrgAccount[org]; found && other.Profile != a.Profile && other.AccountUUID != a.AccountUUID {
				mismatched = append(mismatched, fmt.Sprintf("%s: Claude in Chrome uses a different Claude account than this profile's claude.ai login, which is the account Claude in Chrome uses in %s", name, label(other.Profile)))
			} else {
				// One account can belong to several orgs, so a lone
				// difference is not proof.
				mismatched = append(mismatched, name+": Claude in Chrome likely uses a different Claude account than this profile's claude.ai login")
			}
		}
	}

	var parts []string
	if len(mismatched) > 0 {
		parts = append(parts, strings.Join(mismatched, "; "))
	}
	if len(ok) > 0 {
		parts = append(parts, "matches claude.ai: "+strings.Join(ok, ", "))
	}
	if len(skipped) > 0 {
		parts = append(parts, "not compared: "+strings.Join(skipped, "; "))
	}
	c := Check{Name: claudeAccountsCheckName, Severity: SeverityOK, Detail: strings.Join(parts, ". ")}
	if len(mismatched) > 0 {
		c.Severity = SeverityWarn
		c.Remediation = "In that Chrome profile, sign out of claude.ai and sign back in with the account Claude in Chrome uses (or sign Claude in Chrome into the claude.ai account), then click Reload in the Claude side panel."
	}
	return c
}

// claudeCookieOrg reads the org of a profile's claude.ai web session from
// its lastActiveOrg cookie. Only that cookie is decrypted.
func claudeCookieOrg(root, profile string, decryptKey func() ([]byte, error)) (string, error) {
	path := chromepaths.ProfileCookiesDB(filepath.Join(root, profile))
	if _, err := os.Stat(path); err != nil {
		return "", nil // no cookie store: not signed in anywhere
	}
	key, err := decryptKey()
	if err != nil {
		if chrome.IsKeychainLocked(err) {
			return "", errors.New("keychain locked")
		}
		return "", errors.New("keychain access denied")
	}
	org, found, err := chrome.ReadCookieValue(path, "%claude.ai", "lastActiveOrg", key)
	if err != nil {
		return "", fmt.Errorf("could not read the claude.ai login: %w", err)
	}
	if !found {
		return "", nil
	}
	return strings.TrimSpace(org), nil
}

// chromeProfileLabels maps profile folders to the Google account signed
// into each, or the profile's display name, from Chrome's Local State.
func chromeProfileLabels(root string) map[string]string {
	raw, err := os.ReadFile(filepath.Join(root, "Local State"))
	if err != nil {
		return nil
	}
	var ls struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				UserName string `json:"user_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(raw, &ls) != nil {
		return nil
	}
	out := map[string]string{}
	for dir, info := range ls.Profile.InfoCache {
		if info.UserName != "" {
			out[dir] = info.UserName
		} else {
			out[dir] = info.Name
		}
	}
	return out
}
