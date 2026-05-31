package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestConvergeSafeStorageToOneItem covers the U2 value-preserved collapse:
// no-op on the healthy single-item path, a value-preserving collapse when the
// race left duplicates, and the cookie-safety refuse-to-delete guard when the
// existing value cannot be read.
func TestConvergeSafeStorageToOneItem(t *testing.T) {
	// Save and restore all seams.
	origCount := countSafeStorageItemsFunc
	origPw := safeStoragePasswordFunc
	origUnlock := unlockKeychainFunc
	origSec := execSecurityFunc
	t.Cleanup(func() {
		countSafeStorageItemsFunc = origCount
		safeStoragePasswordFunc = origPw
		unlockKeychainFunc = origUnlock
		execSecurityFunc = origSec
	})

	t.Run("single item is a no-op (proven happy path unchanged)", func(t *testing.T) {
		countSafeStorageItemsFunc = func() (int, error) { return 1, nil }
		var secCalls []string
		execSecurityFunc = func(args ...string) (string, error) {
			secCalls = append(secCalls, strings.Join(args, " "))
			return "", nil
		}
		removed, err := convergeSafeStorageToOneItem("pw")
		if err != nil || removed != 0 {
			t.Fatalf("got (removed=%d, err=%v), want (0, nil)", removed, err)
		}
		if len(secCalls) != 0 {
			t.Errorf("no security calls expected on single item; got %v", secCalls)
		}
	})

	t.Run("zero items (no key yet) is a no-op", func(t *testing.T) {
		countSafeStorageItemsFunc = func() (int, error) { return 0, nil }
		removed, err := convergeSafeStorageToOneItem("pw")
		if err != nil || removed != 0 {
			t.Fatalf("got (removed=%d, err=%v), want (0, nil)", removed, err)
		}
	})

	t.Run("duplicates collapse to one with the SAME value", func(t *testing.T) {
		countSafeStorageItemsFunc = func() (int, error) { return 3, nil }
		unlockKeychainFunc = func(string) error { return nil }
		safeStoragePasswordFunc = func() (string, error) { return "REAL-KEY-VALUE", nil }
		var deletes, adds int
		var addedValue string
		execSecurityFunc = func(args ...string) (string, error) {
			switch args[0] {
			case "delete-generic-password":
				deletes++
			case "add-generic-password":
				adds++
				for i, a := range args {
					if a == "-w" && i+1 < len(args) {
						addedValue = args[i+1]
					}
				}
			}
			return "", nil
		}
		removed, err := convergeSafeStorageToOneItem("pw")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if removed != 2 {
			t.Errorf("removed = %d, want 2 (3 items -> 1)", removed)
		}
		if adds != 1 {
			t.Errorf("re-add count = %d, want exactly 1", adds)
		}
		if addedValue != "REAL-KEY-VALUE" {
			t.Errorf("re-added value = %q, want the preserved original (a changed value wipes all cookies)", addedValue)
		}
		if deletes == 0 {
			t.Errorf("expected duplicate deletes, got 0")
		}
	})

	t.Run("REFUSES to delete when the value can't be read (cookie-safety)", func(t *testing.T) {
		countSafeStorageItemsFunc = func() (int, error) { return 2, nil }
		unlockKeychainFunc = func(string) error { return nil }
		safeStoragePasswordFunc = func() (string, error) { return "", errors.New("not in ACL") }
		var deletes int
		execSecurityFunc = func(args ...string) (string, error) {
			if args[0] == "delete-generic-password" {
				deletes++
			}
			return "", nil
		}
		removed, err := convergeSafeStorageToOneItem("pw")
		if err == nil {
			t.Fatalf("expected a refuse error, got nil (removed=%d)", removed)
		}
		if deletes != 0 {
			t.Errorf("MUST NOT delete when value is unreadable; got %d deletes (would wipe cookies)", deletes)
		}
		if !strings.Contains(err.Error(), "destroy") {
			t.Errorf("refuse error should explain the cookie-destruction risk: %v", err)
		}
	})

	t.Run("count error surfaces and does not delete", func(t *testing.T) {
		countSafeStorageItemsFunc = func() (int, error) { return 0, errors.New("dump failed") }
		var deletes int
		execSecurityFunc = func(args ...string) (string, error) {
			if args[0] == "delete-generic-password" {
				deletes++
			}
			return "", nil
		}
		if _, err := convergeSafeStorageToOneItem("pw"); err == nil {
			t.Fatalf("expected count error")
		}
		if deletes != 0 {
			t.Errorf("no deletes on count error; got %d", deletes)
		}
	})
}
