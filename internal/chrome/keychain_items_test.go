package chrome

import (
	"errors"
	"testing"
)

// sampleDumpOneItem mimics `security dump-keychain` output around one Chrome
// Safe Storage generic-password item. dump-keychain emits the service string
// twice per item (the 0x00000007 label blob and the svce attribute); the
// counter keys on the svce line, which appears exactly once per item.
const sampleDumpOneItem = `keychain: "/Users/x/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    0x00000007 <blob>="Chrome Safe Storage"
    "acct"<blob>="Chrome"
    "svce"<blob>="Chrome Safe Storage"
keychain: "/Users/x/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    0x00000007 <blob>="Some Other Service"
    "svce"<blob>="Some Other Service"
`

const sampleDumpTwoItems = sampleDumpOneItem + `keychain: "/Users/x/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    0x00000007 <blob>="Chrome Safe Storage"
    "acct"<blob>="Chrome"
    "svce"<blob>="Chrome Safe Storage"
`

func TestCountSafeStorageItems(t *testing.T) {
	orig := dumpKeychainRunner
	t.Cleanup(func() { dumpKeychainRunner = orig })

	cases := []struct {
		name string
		out  string
		err  error
		want int
		fail bool
	}{
		{"one item", sampleDumpOneItem, nil, 1, false},
		{"two items is the race signature", sampleDumpTwoItems, nil, 2, false},
		{"zero items when chrome never wrote a key", "class: \"genp\"\n    \"svce\"<blob>=\"Other\"\n", nil, 0, false},
		{"output present despite nonzero exit still counts", sampleDumpOneItem, errors.New("exit 1"), 1, false},
		{"empty output with error fails", "", errors.New("boom"), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dumpKeychainRunner = func() (string, error) { return tc.out, tc.err }
			got, err := CountSafeStorageItems()
			if tc.fail {
				if err == nil {
					t.Fatalf("expected error, got nil (n=%d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIsKeychainLocked(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not locked", nil, false},
		{"explicit -25308 code", errors.New("keybase keychain GetGenericPassword: ... (-25308)"), true},
		{"interaction-not-allowed text", errors.New("User interaction is not allowed."), true},
		{"unrelated error is not locked", errors.New("not readable by this process"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsKeychainLocked(tc.err); got != tc.want {
				t.Errorf("IsKeychainLocked(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
