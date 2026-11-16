package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

const (
	orgEsper = "adce0660-6e46-4b48-996d-ad7059e69d99"
	orgOther = "9ba6dfe3-7a4a-4432-8074-53d61dc50d5e"
)

func claudeAccountsTestEnv() claudeAccountsEnv {
	return claudeAccountsEnv{
		accounts: func() ([]chrome.ClaudeExtAccount, []error) {
			return []chrome.ClaudeExtAccount{
				{Profile: "Default", AccountUUID: "acct-gmail", TokenOrg: orgOther},
				{Profile: "Profile 1", AccountUUID: "acct-esper", TokenOrg: orgEsper},
			}, nil
		},
		cookieReadAllowed: true,
		cookieOrg: func(profile string) (string, error) {
			return map[string]string{"Default": orgOther, "Profile 1": orgEsper}[profile], nil
		},
		labels: func() map[string]string {
			return map[string]string{"Default": "mvanhorn@gmail.com", "Profile 1": "Person 1"}
		},
	}
}

func TestClaudeAccountsCheck(t *testing.T) {
	cases := []struct {
		name     string
		mod      func(*claudeAccountsEnv)
		severity Severity
		want     []string
		notWant  []string
	}{
		{
			name:     "every profile matches its claude.ai login",
			mod:      func(*claudeAccountsEnv) {},
			severity: SeverityOK,
			want:     []string{"Default", "Profile 1"},
		},
		{
			// The 2026-11-16 state: Default's extension is on the other
			// account while its claude.ai login is the Profile 1 account.
			name: "cross match is a confirmed mismatch",
			mod: func(e *claudeAccountsEnv) {
				e.cookieOrg = func(string) (string, error) { return orgEsper, nil }
			},
			severity: SeverityWarn,
			want:     []string{"Default (mvanhorn@gmail.com)", "uses a different Claude account", "Profile 1"},
		},
		{
			name: "lone org difference is a likely mismatch",
			mod: func(e *claudeAccountsEnv) {
				e.cookieOrg = func(p string) (string, error) {
					if p == "Default" {
						return "11111111-0000-0000-0000-000000000000", nil
					}
					return orgEsper, nil
				}
			},
			severity: SeverityWarn,
			want:     []string{"Default", "likely uses a different Claude account"},
			notWant:  []string{"Profile 1 (Person 1): Claude in Chrome"},
		},
		{
			name: "skip_chrome_sqlite never reads cookies",
			mod: func(e *claudeAccountsEnv) {
				e.cookieReadAllowed = false
				e.cookieOrg = func(string) (string, error) {
					t.Fatal("cookie read with skip_chrome_sqlite set")
					return "", nil
				}
			},
			severity: SeverityOK,
			want:     []string{"skip_chrome_sqlite"},
		},
		{
			name: "unreadable cookie skips that profile",
			mod: func(e *claudeAccountsEnv) {
				e.cookieOrg = func(p string) (string, error) {
					if p == "Default" {
						return "", errors.New("keychain access denied")
					}
					return orgEsper, nil
				}
			},
			severity: SeverityOK,
			want:     []string{"Default", "keychain access denied"},
		},
		{
			name: "profile without tokenOrg is skipped, not a mismatch",
			mod: func(e *claudeAccountsEnv) {
				e.accounts = func() ([]chrome.ClaudeExtAccount, []error) {
					return []chrome.ClaudeExtAccount{{Profile: "Default", AccountUUID: "a"}}, nil
				}
			},
			severity: SeverityOK,
			want:     []string{"Default"},
		},
		{
			name: "one profile still runs",
			mod: func(e *claudeAccountsEnv) {
				e.accounts = func() ([]chrome.ClaudeExtAccount, []error) {
					return []chrome.ClaudeExtAccount{{Profile: "Profile 1", AccountUUID: "a", TokenOrg: orgEsper}}, nil
				}
			},
			severity: SeverityOK,
			want:     []string{"Profile 1"},
		},
		{
			name: "no signed-in extension",
			mod: func(e *claudeAccountsEnv) {
				e.accounts = func() ([]chrome.ClaudeExtAccount, []error) { return nil, nil }
			},
			severity: SeveritySkipped,
		},
		{
			name: "extension stores unreadable",
			mod: func(e *claudeAccountsEnv) {
				e.accounts = func() ([]chrome.ClaudeExtAccount, []error) {
					return nil, []error{errors.New("Default: permission denied")}
				}
			},
			severity: SeveritySkipped,
			want:     []string{"permission denied"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := claudeAccountsTestEnv()
			c.mod(&env)
			got := checkClaudeAccountsWith(env)
			if got.Severity != c.severity {
				t.Fatalf("severity %s, want %s (detail %q)", got.Severity, c.severity, got.Detail)
			}
			text := got.Detail + " " + got.Remediation
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("want %q in %q", w, text)
				}
			}
			for _, w := range c.notWant {
				if strings.Contains(text, w) {
					t.Errorf("did not want %q in %q", w, text)
				}
			}
		})
	}
}
