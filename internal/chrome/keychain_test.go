package chrome

import (
	"errors"
	"slices"
	"testing"
)

func TestSafeStoragePasswordFor_SkipsCGOForUnsignedBinary(t *testing.T) {
	origCodesign := codesignRunner
	origKeybase := safeStorageViaKeybaseRunner
	t.Cleanup(func() {
		codesignRunner = origCodesign
		safeStorageViaKeybaseRunner = origKeybase
	})

	codesignRunner = func(string) (string, error) {
		return "path/to/bin: code object is not signed at all", nil
	}
	keybaseCalled := false
	safeStorageViaKeybaseRunner = func(_, _ string) (string, error) {
		keybaseCalled = true
		return "", nil
	}

	b, _ := LookupBrowser("")
	SafeStoragePasswordFor(b) //nolint:errcheck
	if keybaseCalled {
		t.Error("safeStorageViaKeybaseRunner must not be called for unsigned binaries")
	}
}

func TestSafeStoragePasswordFor_UsesCGOForSignedBinary(t *testing.T) {
	origCodesign := codesignRunner
	origKeybase := safeStorageViaKeybaseRunner
	t.Cleanup(func() {
		codesignRunner = origCodesign
		safeStorageViaKeybaseRunner = origKeybase
	})

	codesignRunner = func(string) (string, error) {
		return "TeamIdentifier=ABC1234DEF", nil
	}
	keybaseCalled := false
	safeStorageViaKeybaseRunner = func(_, _ string) (string, error) {
		keybaseCalled = true
		return "test-password", nil
	}

	b, _ := LookupBrowser("")
	pw, err := SafeStoragePasswordFor(b)
	if err != nil {
		t.Fatalf("expected success from keybase stub, got: %v", err)
	}
	if !keybaseCalled {
		t.Error("safeStorageViaKeybaseRunner must be called for signed binaries")
	}
	if pw != "test-password" {
		t.Errorf("password = %q, want %q", pw, "test-password")
	}
}

func TestSafeStoragePasswordFor_BinaryTeamIDErrorTreatedAsUnsigned(t *testing.T) {
	origCodesign := codesignRunner
	origKeybase := safeStorageViaKeybaseRunner
	t.Cleanup(func() {
		codesignRunner = origCodesign
		safeStorageViaKeybaseRunner = origKeybase
	})

	// Non-unsigned codesign error (e.g., binary not found) → treat as unsigned.
	codesignRunner = func(string) (string, error) {
		return "", errors.New("codesign: no such file")
	}
	keybaseCalled := false
	safeStorageViaKeybaseRunner = func(_, _ string) (string, error) {
		keybaseCalled = true
		return "", nil
	}

	b, _ := LookupBrowser("")
	SafeStoragePasswordFor(b) //nolint:errcheck
	if keybaseCalled {
		t.Error("safeStorageViaKeybaseRunner must not be called when BinaryTeamID errors")
	}
}

func TestIsKeychainAccessError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic error", errors.New("network timeout"), false},
		{"did you grant access", errors.New("read Chrome Safe Storage from Keychain (did you grant access?): exit status 1"), true},
		{"not yet in partition", errors.New("read Chrome Safe Storage from Keychain timed out after 10s; this binary is not yet in the Safe Storage partition."), true},
		// Locked keychain (-25308) is transient, not a missing grant: launchd
		// should retry, so this must NOT be classified as an access error.
		{"keychain locked 25308 is not access error", errors.New("error -25308: User interaction is not allowed"), false},
		{"keychain locked phrase is not access error", errors.New("interaction is not allowed"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsKeychainAccessError(tc.err); got != tc.want {
				t.Errorf("IsKeychainAccessError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestBuildPartitionListArgv_DefaultsToDefaultPartitionList(t *testing.T) {
	got := buildPartitionListArgv("", "")
	want := []string{
		"set-generic-password-partition-list",
		"-S", DefaultPartitionList,
		"-s", "Chrome Safe Storage",
		"-a", "Chrome",
	}
	if !slices.Equal(got, want) {
		t.Errorf("default argv: got %v, want %v", got, want)
	}
}

func TestBuildPartitionListArgv_CustomPartitionsPassthrough(t *testing.T) {
	custom := "apple-tool:,apple:,teamid:ABC1234"
	got := buildPartitionListArgv(custom, "")
	if got[2] != custom {
		t.Errorf("partition list arg: got %q, want %q", got[2], custom)
	}
	// Service and account stay pinned regardless of partition input.
	if got[4] != "Chrome Safe Storage" {
		t.Errorf("service arg: got %q", got[4])
	}
	if got[6] != "Chrome" {
		t.Errorf("account arg: got %q", got[6])
	}
}

func TestBuildPartitionListArgv_SubcommandIsFirst(t *testing.T) {
	// The `security` CLI dispatches on argv[0]. Mis-ordering here would
	// silently hit the wrong subcommand.
	got := buildPartitionListArgv("", "")
	if got[0] != "set-generic-password-partition-list" {
		t.Errorf("subcommand arg[0]: got %q, want set-generic-password-partition-list", got[0])
	}
}
