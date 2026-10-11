package cli

import (
	"errors"
	"strings"
	"testing"
)

// recreateFakes stubs every Keychain seam the delete-and-recreate-with-T
// strategy touches and records each `security` call, so no test reaches
// the real Keychain.
type recreateFakes struct{ calls [][]string }

func stubRecreateKeychain(t *testing.T, items int, read func() (string, error)) *recreateFakes {
	t.Helper()
	f := &recreateFakes{}
	origCount, origPw, origSec, origMaster := countSafeStorageItemsFunc, safeStoragePasswordFunc, execSecurityFunc, createMasterKeyFunc
	t.Cleanup(func() {
		countSafeStorageItemsFunc, safeStoragePasswordFunc, execSecurityFunc, createMasterKeyFunc = origCount, origPw, origSec, origMaster
	})
	countSafeStorageItemsFunc = func() (int, error) { return items, nil }
	safeStoragePasswordFunc = read
	execSecurityFunc = func(args ...string) (string, error) {
		f.calls = append(f.calls, append([]string(nil), args...))
		return "ok", nil
	}
	createMasterKeyFunc = func(string, []string) error { return nil }
	return f
}

func (f *recreateFakes) called(verb string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

func addedValue(t *testing.T, call []string) string {
	t.Helper()
	for i, a := range call {
		if a == "-w" && i+1 < len(call) {
			return call[i+1]
		}
	}
	t.Fatalf("add-generic-password call has no -w value: %v", call)
	return ""
}

func recreateWithT(t *testing.T, anyApp bool) kcStrategy {
	t.Helper()
	for _, s := range buildStrategies(nil, false, anyApp) {
		if s.name == "delete-and-recreate-with-T" {
			return s
		}
	}
	t.Fatal("delete-and-recreate-with-T strategy not built")
	return kcStrategy{}
}

// An existing item is recreated with its own value, so existing Chrome
// cookies stay decryptable.
func TestRecreateWithT_PreservesExistingValue(t *testing.T) {
	f := stubRecreateKeychain(t, 1, func() (string, error) { return "REAL-KEY-VALUE", nil })
	if _, err := recreateWithT(t, false).apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	adds := f.called("add-generic-password")
	if len(adds) != 1 {
		t.Fatalf("add-generic-password calls = %d, want 1", len(adds))
	}
	if got := addedValue(t, adds[0]); got != "REAL-KEY-VALUE" {
		t.Errorf("recreated with value %q, want the existing REAL-KEY-VALUE", got)
	}
}

// An existing item whose value cannot be read is never deleted: recreating
// it with a different value would destroy every existing Chrome cookie.
func TestRecreateWithT_RefusesWhenExistingValueUnreadable(t *testing.T) {
	f := stubRecreateKeychain(t, 1, func() (string, error) { return "", errors.New("no grant") })
	_, err := recreateWithT(t, false).apply()
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("apply err = %v, want a refusal", err)
	}
	if n := len(f.called("delete-generic-password")); n != 0 {
		t.Errorf("delete-generic-password called %d time(s); want 0", n)
	}
	if n := len(f.called("add-generic-password")); n != 0 {
		t.Errorf("add-generic-password called %d time(s); want 0", n)
	}
}

// With no item yet there are no cookies to lose, so a fresh random value
// is used.
func TestRecreateWithT_FreshKeychainGetsNewValue(t *testing.T) {
	f := stubRecreateKeychain(t, 0, func() (string, error) { return "", errors.New("not found") })
	if _, err := recreateWithT(t, false).apply(); err != nil {
		t.Fatalf("apply: %v", err)
	}
	adds := f.called("add-generic-password")
	if len(adds) != 1 || addedValue(t, adds[0]) == "" {
		t.Fatalf("want one add with a non-empty value, got %v", adds)
	}
}

// --any-app's guard refuses when the value is unreadable; the strategy the
// loop falls through to must refuse too, or the guard is bypassed.
func TestAnyAppChain_UnreadableValueDeletesNothing(t *testing.T) {
	f := stubRecreateKeychain(t, 1, func() (string, error) { return "", errors.New("no grant") })
	for _, s := range buildStrategies(nil, false, true) {
		if !strings.HasPrefix(s.name, "delete-and-recreate") {
			continue
		}
		if _, err := s.apply(); err == nil {
			t.Errorf("%s succeeded with an unreadable existing value", s.name)
		}
	}
	if n := len(f.called("delete-generic-password")); n != 0 {
		t.Errorf("delete-generic-password called %d time(s) across the --any-app chain; want 0", n)
	}
}
