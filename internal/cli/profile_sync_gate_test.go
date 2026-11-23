package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// profileSyncFixture is a fake Chrome under a temp HOME: a source profile
// (Default) holding one cookie and an empty target profile (Profile 2), with
// the Keychain and lsof seams stubbed.
type profileSyncFixture struct {
	t       *testing.T
	target  string // target profile directory
	holders []fileHolder
	out     *bytes.Buffer
	key     []byte
}

func newProfileSyncFixture(t *testing.T) *profileSyncFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldDir := common.ConfigDir
	common.ConfigDir = t.TempDir()
	oldPw, oldHolders := profileSyncPasswordFor, profileSyncHolders
	oldFrom, oldTo, oldDry, oldAllow := profileSyncFrom, profileSyncTo, profileSyncDryRun, profileSyncAllowSignout
	t.Cleanup(func() {
		common.ConfigDir = oldDir
		profileSyncPasswordFor, profileSyncHolders = oldPw, oldHolders
		profileSyncFrom, profileSyncTo, profileSyncDryRun, profileSyncAllowSignout = oldFrom, oldTo, oldDry, oldAllow
		profileSyncCmd.SetErr(nil)
	})

	browser, err := chrome.LookupBrowser("")
	if err != nil {
		t.Fatal(err)
	}
	profileSyncPasswordFor = func(chrome.Browser) (string, error) { return "test-password", nil }
	key, err := chrome.DeriveAESKey("test-password")
	if err != nil {
		t.Fatal(err)
	}

	f := &profileSyncFixture{t: t, target: browser.ProfileDir("Profile 2"), out: &bytes.Buffer{}, key: key}
	for _, dir := range []string{browser.ProfileDir("Default"), f.target} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	seedSourceCookiesDB(t, browser.CookiesPath("Default"), []chrome.Cookie{
		{HostKey: ".example.com", Name: "sid", Value: "v1", Path: "/", IsSecure: 1},
	}, key)
	seedSourceCookiesDB(t, browser.CookiesPath("Profile 2"), nil, key)

	profileSyncHolders = func(string) ([]fileHolder, error) { return f.holders, nil }
	profileSyncFrom, profileSyncTo = "Default", "Profile 2"
	profileSyncDryRun, profileSyncAllowSignout = false, false
	profileSyncCmd.SetErr(f.out)
	return f
}

// boundSessions writes the target's Device Bound Sessions store with these
// schemeful-site keys, as Chrome does.
func (f *profileSyncFixture) boundSessions(keys ...string) {
	f.t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(f.target, "Device Bound Sessions"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE dbsc_session_tbl (key TEXT, proto BLOB, PRIMARY KEY(key))`); err != nil {
		f.t.Fatal(err)
	}
	for _, k := range keys {
		if _, err := db.Exec(`INSERT INTO dbsc_session_tbl VALUES (?, x'00')`, k); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *profileSyncFixture) targetHosts() []string {
	f.t.Helper()
	browser, _ := chrome.LookupBrowser("")
	got, err := chrome.ReadCookiesForHostClosed(browser.CookiesPath("Profile 2"), "%", f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	return hostsFromChromeCookies(got)
}

var (
	agentcookieHolders = []fileHolder{{PID: 101, Command: "agentcookie"}, {PID: 102, Command: "agentcookie"}}
	chromeHolder       = fileHolder{PID: 15699, Command: "Google Chrome Helper (Aperitif)"}
)

func TestParseLsofHolders(t *testing.T) {
	out := "p15699\ncGoogle Chrome Helper (Aperitif)\nftxt\nf66\np77149\ncagentcookie\nf30\n"
	got := parseLsofHolders(out)
	want := []fileHolder{chromeHolder, {PID: 77149, Command: "agentcookie"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("holders = %#v, want %#v", got, want)
	}
}

func TestBlockingHolders(t *testing.T) {
	self := os.Getpid()
	cases := []struct {
		name string
		in   []fileHolder
		want []fileHolder
	}{
		{"only agentcookie daemons", agentcookieHolders, nil},
		{"this process", []fileHolder{{PID: self, Command: "profile-sync.test"}}, nil},
		{"chrome and agentcookie", append([]fileHolder{chromeHolder}, agentcookieHolders...), []fileHolder{chromeHolder}},
		{"unknown program", []fileHolder{{PID: 9, Command: "sqlite3"}}, []fileHolder{{PID: 9, Command: "sqlite3"}}},
	}
	for _, tc := range cases {
		got := blockingHolders(tc.in, self)
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestRunProfileSync_AgentcookieHoldersDoNotBlock(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.holders = agentcookieHolders
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatalf("run blocked by agentcookie's own readers: %v", err)
	}
	if got := f.targetHosts(); !reflect.DeepEqual(got, []string{".example.com"}) {
		t.Fatalf("target cookies = %v", got)
	}
}

func TestRunProfileSync_ChromeHolderBlocksAndIsNamed(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.holders = append([]fileHolder{chromeHolder}, agentcookieHolders...)
	err := runProfileSync(profileSyncCmd, nil)
	if err == nil {
		t.Fatal("want a refusal while Chrome holds the target")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Google Chrome Helper (Aperitif) (pid 15699)") || strings.Contains(msg, "agentcookie (pid") {
		t.Fatalf("refusal should name only the Chrome holder: %s", msg)
	}
	if !strings.Contains(msg, "close every window") {
		t.Fatalf("no device-bound sites: want today's close instructions: %s", msg)
	}
}

func TestRunProfileSync_OpenWithBoundSessionsNeedsFlag(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.holders = []fileHolder{chromeHolder}
	f.boundSessions("https://claude.ai", "https://google.com")

	err := runProfileSync(profileSyncCmd, nil)
	if err == nil {
		t.Fatal("want a stop before close instructions")
	}
	msg := err.Error()
	for _, want := range []string{"claude.ai", "google.com", "Claude in Chrome", "--allow-signout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "close every window") {
		t.Errorf("close instructions given without --allow-signout: %s", msg)
	}

	profileSyncAllowSignout = true
	err = runProfileSync(profileSyncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "close every window") || !strings.Contains(err.Error(), "pid 15699") {
		t.Fatalf("with --allow-signout want close instructions naming the holder, got %v", err)
	}
}

func TestRunProfileSync_ClosedWithBoundSessionsStopsBeforeWrite(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.boundSessions("https://google.com")

	err := runProfileSync(profileSyncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "google.com") || !strings.Contains(err.Error(), "--allow-signout") {
		t.Fatalf("want a stop naming google.com and the flag, got %v", err)
	}
	if strings.Contains(err.Error(), "Claude in Chrome") {
		t.Errorf("no claude.ai session, so Claude in Chrome should not be mentioned: %s", err)
	}
	if got := f.targetHosts(); len(got) != 0 {
		t.Fatalf("wrote without --allow-signout: %v", got)
	}

	profileSyncAllowSignout = true
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatalf("with --allow-signout: %v", err)
	}
	if got := f.targetHosts(); !reflect.DeepEqual(got, []string{".example.com"}) {
		t.Fatalf("target cookies = %v", got)
	}
}

func TestRunProfileSync_UnreadableBoundStoreIsGated(t *testing.T) {
	f := newProfileSyncFixture(t)
	if err := os.WriteFile(filepath.Join(f.target, "Device Bound Sessions"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runProfileSync(profileSyncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--allow-signout") {
		t.Fatalf("unreadable store must be gated like found sites, got %v", err)
	}
	if got := f.targetHosts(); len(got) != 0 {
		t.Fatalf("wrote without --allow-signout: %v", got)
	}
	profileSyncAllowSignout = true
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatalf("with --allow-signout: %v", err)
	}
}

func TestRunProfileSync_DryRunReportsBoundSitesWithoutFlag(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.holders = []fileHolder{chromeHolder}
	f.boundSessions("https://claude.ai")
	profileSyncDryRun = true
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "claude.ai") || !strings.Contains(out, "a real run needs --allow-signout") || !strings.Contains(out, "dry-run; nothing written") {
		t.Fatalf("dry run output: %s", out)
	}
}

func TestRunProfileSync_NoBoundSessionsAddsNothing(t *testing.T) {
	f := newProfileSyncFixture(t)
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.out.String(), "sign") {
		t.Fatalf("unexpected sign-out output: %s", f.out.String())
	}
}

func TestRunProfileSync_ReopenedBeforeWriteStops(t *testing.T) {
	f := newProfileSyncFixture(t)
	calls := 0
	profileSyncHolders = func(string) ([]fileHolder, error) {
		calls++
		if calls == 1 {
			return agentcookieHolders, nil
		}
		return append([]fileHolder{chromeHolder}, agentcookieHolders...), nil
	}
	err := runProfileSync(profileSyncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "reopened") || !strings.Contains(err.Error(), "pid 15699") {
		t.Fatalf("want a stop naming the Chrome holder that reopened the target, got %v", err)
	}
	if got := f.targetHosts(); len(got) != 0 {
		t.Fatalf("wrote after the target was reopened: %v", got)
	}
}

func TestRunProfileSync_ReopenedWithNewBoundSessionsIsGated(t *testing.T) {
	f := newProfileSyncFixture(t)
	calls := 0
	profileSyncHolders = func(string) ([]fileHolder, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		f.boundSessions("https://claude.ai")
		return []fileHolder{chromeHolder}, nil
	}
	err := runProfileSync(profileSyncCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "reopened") || !strings.Contains(err.Error(), "claude.ai") || !strings.Contains(err.Error(), "--allow-signout") {
		t.Fatalf("want the reopened refusal to carry the new sign-out risk, got %v", err)
	}
	if got := f.targetHosts(); len(got) != 0 {
		t.Fatalf("wrote after the target was reopened: %v", got)
	}
}

func TestRunProfileSync_AllowSignoutStillNamesSites(t *testing.T) {
	f := newProfileSyncFixture(t)
	f.boundSessions("https://google.com")
	profileSyncAllowSignout = true
	if err := runProfileSync(profileSyncCmd, nil); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "google.com") || !strings.Contains(out, "--allow-signout") {
		t.Fatalf("accepted risk should still be shown: %s", out)
	}
}
