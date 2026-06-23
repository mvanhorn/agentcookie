package chrome

import (
	"context"
	"errors"
	"fmt"
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

func TestSafeStoragePasswordFor_LockedKeychainShortCircuitsBeforeCLI(t *testing.T) {
	// A locked login keychain must short-circuit to ErrKeychainLocked BEFORE the
	// `security` CLI runs -- otherwise the CLI hangs on an unlock prompt until the
	// timeout and the error gets misclassified as a missing grant (exit 0). The
	// transient lock must let a --watch agent exit non-zero so launchd retries.
	origCodesign, origKeybase, origLocked, origCLI := codesignRunner, safeStorageViaKeybaseRunner, keychainLockedCheck, securityCLIRead
	t.Cleanup(func() {
		codesignRunner, safeStorageViaKeybaseRunner, keychainLockedCheck, securityCLIRead = origCodesign, origKeybase, origLocked, origCLI
	})

	codesignRunner = func(string) (string, error) { return "code object is not signed at all", nil } // unsigned: skip CGO
	keychainLockedCheck = func() (bool, error) { return true, nil }                                  // keychain is locked
	cliCalled := false
	securityCLIRead = func(_ context.Context, _, _ string) ([]byte, []byte, error) {
		cliCalled = true
		return nil, nil, nil
	}

	b, _ := LookupBrowser("")
	_, err := SafeStoragePasswordFor(b)
	if !errors.Is(err, ErrKeychainLocked) {
		t.Errorf("locked keychain should return ErrKeychainLocked, got: %v", err)
	}
	if IsKeychainAccessError(err) {
		t.Error("a locked keychain must not be classified as an access error (it should retry)")
	}
	if cliCalled {
		t.Error("security CLI must not run when the keychain is locked (avoids hang/unlock-prompt storm)")
	}
}

func TestSafeStoragePasswordFor_UnknownLockStateFallsThroughToCLI(t *testing.T) {
	// When lock state is unknown (status error, or non-darwin/cgo build), do not
	// short-circuit -- fall through to the CLI lane.
	origCodesign, origKeybase, origLocked, origCLI := codesignRunner, safeStorageViaKeybaseRunner, keychainLockedCheck, securityCLIRead
	t.Cleanup(func() {
		codesignRunner, safeStorageViaKeybaseRunner, keychainLockedCheck, securityCLIRead = origCodesign, origKeybase, origLocked, origCLI
	})

	codesignRunner = func(string) (string, error) { return "code object is not signed at all", nil }
	keychainLockedCheck = func() (bool, error) { return false, errors.New("status unavailable") }
	cliCalled := false
	securityCLIRead = func(_ context.Context, _, _ string) ([]byte, []byte, error) {
		cliCalled = true
		return []byte("pw\n"), nil, nil
	}

	b, _ := LookupBrowser("")
	pw, err := SafeStoragePasswordFor(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pw != "pw" {
		t.Errorf("password = %q, want %q", pw, "pw")
	}
	if !cliCalled {
		t.Error("unknown lock state should fall through to the security CLI")
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
		{"missing grant sentinel", fmt.Errorf("read Keychain (did you grant access?): %w", ErrKeychainNoGrant), true},
		{"timeout sentinel", fmt.Errorf("read Keychain timed out: %w", ErrKeychainTimeout), true},
		// Sentinel survives an extra wrap layer -- the boundary the old string
		// match defeated: classification holds even after re-wrapping.
		{"missing grant wrapped twice", fmt.Errorf("cmux-sync: %w", fmt.Errorf("read Keychain: %w", ErrKeychainNoGrant)), true},
		// The PR #107 regression guard: a LOCKED error wrapped in the
		// "did you grant access?" prose must NOT be classified as an access
		// error -- it carries ErrKeychainLocked, so launchd should retry.
		{"locked wrapped in grant prose is not access error", fmt.Errorf("read Keychain (did you grant access?): login keychain is locked: %w", ErrKeychainLocked), false},
		{"raw locked string is not access error", errors.New("error -25308: User interaction is not allowed"), false},
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
