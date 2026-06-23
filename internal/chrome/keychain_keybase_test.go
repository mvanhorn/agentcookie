//go:build darwin && cgo

package chrome

import (
	"testing"
	"time"
)

// TestSafeStoragePasswordViaKeybase_ReturnsPromptly is the regression guard for
// the #106 leak: with kSecUseAuthenticationUIFail the SecItem call can never
// block on a SecurityAgent prompt, so it must return quickly whether or not
// this test binary is in the Safe Storage partition. If this ever hangs, the
// no-UI query attribute regressed and the goroutine-leak bug is back.
func TestSafeStoragePasswordViaKeybase_ReturnsPromptly(t *testing.T) {
	type res struct {
		pw  string
		err error
	}
	done := make(chan res, 1)
	go func() {
		pw, err := safeStoragePasswordViaKeybaseFor(keychainService, keychainAccount)
		done <- res{pw, err}
	}()

	select {
	case r := <-done:
		// Either outcome is valid and environment-dependent:
		//   - granted (binary in the partition): success, non-empty password
		//   - not granted / locked: ErrKeychainInteractionRequired (no prompt)
		// The contract under test is "no hang + a usable key on success", not
		// which branch this particular machine takes.
		if r.err == nil && r.pw == "" {
			t.Error("success path returned an empty Safe Storage password")
		}
		if r.err == nil {
			if _, err := DeriveAESKey(r.pw); err != nil {
				t.Errorf("password from keychain did not derive an AES key: %v", err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("safeStoragePasswordViaKeybaseFor hung; kSecUseAuthenticationUIFail should make it non-blocking")
	}
}
