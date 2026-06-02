package chrome

import (
	"errors"
	"strings"
	"testing"
)

func TestTeamPartitionList(t *testing.T) {
	if got := TeamPartitionList("NM8VT393AR"); got != "apple-tool:,apple:,teamid:NM8VT393AR" {
		t.Errorf("TeamPartitionList(team) = %q", got)
	}
	if got := TeamPartitionList(""); got != DefaultPartitionList {
		t.Errorf("TeamPartitionList(\"\") = %q, want DefaultPartitionList %q", got, DefaultPartitionList)
	}
}

func TestBuildPartitionListArgv_WithPassword(t *testing.T) {
	argv := buildPartitionListArgv("apple-tool:,apple:,teamid:NM8VT393AR", "hunter2")
	joined := strings.Join(argv, " ")
	// -k must immediately precede the password value.
	found := false
	for i, a := range argv {
		if a == "-k" {
			if i+1 >= len(argv) || argv[i+1] != "hunter2" {
				t.Fatalf("-k not immediately followed by password: %v", argv)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("expected -k in argv with a password, got %v", argv)
	}
	for _, want := range []string{"set-generic-password-partition-list", "-S", "teamid:NM8VT393AR", keychainService, keychainAccount} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %v", want, argv)
		}
	}
}

func TestBuildPartitionListArgv_NoPasswordOmitsK(t *testing.T) {
	argv := buildPartitionListArgv("apple-tool:,apple:", "")
	for i, a := range argv {
		if a == "-k" {
			t.Fatalf("empty password must omit -k entirely, got %v at %d", argv, i)
		}
		if a == "" {
			t.Fatalf("argv must not contain an empty element: %v", argv)
		}
	}
}

func TestBuildPartitionListArgv_EmptyPartitionsDefaults(t *testing.T) {
	argv := buildPartitionListArgv("", "")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, DefaultPartitionList) {
		t.Errorf("empty partitions should default to %q: %v", DefaultPartitionList, argv)
	}
}

func TestParseTeamIdentifier(t *testing.T) {
	cases := []struct {
		name, out, want string
	}{
		{"devid", "Executable=/x\nIdentifier=agentcookie\nTeamIdentifier=NM8VT393AR\nSealed Resources=none\n", "NM8VT393AR"},
		{"adhoc_not_set", "Identifier=agentcookie\nTeamIdentifier=not set\n", ""},
		{"no_line", "Identifier=agentcookie\nFormat=Mach-O thin\n", ""},
		{"whitespace", "  TeamIdentifier=ABCDE12345  \n", "ABCDE12345"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseTeamIdentifier(c.out); got != c.want {
				t.Errorf("parseTeamIdentifier(%q) = %q, want %q", c.out, got, c.want)
			}
		})
	}
}

func TestBinaryTeamID_DevID(t *testing.T) {
	orig := codesignRunner
	defer func() { codesignRunner = orig }()
	codesignRunner = func(string) (string, error) {
		return "Identifier=agentcookie\nTeamIdentifier=NM8VT393AR\n", nil
	}
	team, err := BinaryTeamID("/usr/bin/whatever")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if team != "NM8VT393AR" {
		t.Errorf("team = %q, want NM8VT393AR", team)
	}
}

func TestBinaryTeamID_AdHocReturnsEmptyNoError(t *testing.T) {
	orig := codesignRunner
	defer func() { codesignRunner = orig }()
	// codesign exits non-zero AND reports no team for an ad-hoc/unsigned binary,
	// but because the team is parseable-as-absent we return ("", nil) so the
	// caller can fall back to the team-less partition list cleanly.
	codesignRunner = func(string) (string, error) {
		return "test-binary: code object is not signed at all\n", errors.New("exit status 1")
	}
	team, err := BinaryTeamID("/tmp/adhoc")
	if err != nil {
		t.Fatalf("ad-hoc binary should not error, got %v", err)
	}
	if team != "" {
		t.Errorf("ad-hoc team should be empty, got %q", team)
	}
}

func TestSetSafeStoragePartitionListWithPassword_RequiresPassword(t *testing.T) {
	if err := SetSafeStoragePartitionListWithPassword("apple-tool:,apple:", ""); err == nil {
		t.Error("expected error when login password is empty")
	}
}

func TestSetSafeStoragePartitionListWithPassword_PassesArgvAndRedacts(t *testing.T) {
	orig := partitionListRunner
	defer func() { partitionListRunner = orig }()
	var gotArgv []string
	partitionListRunner = func(argv []string) (string, error) {
		gotArgv = argv
		// Simulate security echoing the password back in an error; it must be redacted.
		return "failed for key s3cr3t", errors.New("exit status 1")
	}
	err := SetSafeStoragePartitionListWithPassword("apple-tool:,apple:,teamid:NM8VT393AR", "s3cr3t")
	if err == nil {
		t.Fatal("expected error from runner")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("error text leaked the password: %v", err)
	}
	if !strings.Contains(strings.Join(gotArgv, " "), "teamid:NM8VT393AR") {
		t.Errorf("argv missing team partition: %v", gotArgv)
	}
	if !strings.Contains(strings.Join(gotArgv, " "), "-k") {
		t.Errorf("argv missing -k: %v", gotArgv)
	}
}

func TestSetSafeStoragePartitionListWithPassword_Success(t *testing.T) {
	orig := partitionListRunner
	defer func() { partitionListRunner = orig }()
	partitionListRunner = func([]string) (string, error) { return "", nil }
	if err := SetSafeStoragePartitionListWithPassword("apple-tool:,apple:,teamid:X", "pw"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestSafeStorageRemediation_PointsToOnePasswordNotGUI is the U4 guard: the
// daemon's read-failure guidance must point a headless sink at the
// one-password SSH command, never the obsolete GUI "Always Allow" click.
func TestSafeStorageRemediation_PointsToOnePasswordNotGUI(t *testing.T) {
	if !strings.Contains(SafeStorageRemediation, "set-keychain-access") {
		t.Errorf("remediation should name the one-password command: %q", SafeStorageRemediation)
	}
	if !strings.Contains(SafeStorageRemediation, "AGENTCOOKIE_LOGIN_PASSWORD") {
		t.Errorf("remediation should mention the env override: %q", SafeStorageRemediation)
	}
	if strings.Contains(SafeStorageRemediation, "Always Allow") || strings.Contains(SafeStorageRemediation, "Keychain Access") {
		t.Errorf("remediation must not send a headless sink to a GUI prompt: %q", SafeStorageRemediation)
	}
}

func TestSafeStorageRemediationForChromeUsesWizard(t *testing.T) {
	b, err := LookupBrowser("chrome")
	if err != nil {
		t.Fatal(err)
	}
	if got := safeStorageRemediationFor(b); got != SafeStorageRemediation {
		t.Errorf("chrome remediation = %q, want %q", got, SafeStorageRemediation)
	}
}

func TestSafeStorageRemediationForNonChromeAvoidsChromeWizard(t *testing.T) {
	b, err := LookupBrowser("atlas")
	if err != nil {
		t.Fatal(err)
	}
	got := safeStorageRemediationFor(b)
	if !strings.Contains(got, `grant agentcookie read access to the "Atlas Safe Storage" Keychain item`) {
		t.Errorf("atlas remediation should name the Atlas Keychain item: %q", got)
	}
	if strings.Contains(got, "AGENTCOOKIE_LOGIN_PASSWORD") {
		t.Errorf("atlas remediation must not point at Chrome wizard env flow: %q", got)
	}
}
