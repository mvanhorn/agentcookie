//go:build darwin && cgo

// Package chrome's probe_keybase.go exercises Chrome Safe Storage via the
// keybase/go-keychain CGO library -- the same API path kooky's CGO build
// uses. The legacy `security` CLI honors the `-A` "any application" ACL
// flag, but kooky-CGO goes through SecItemCopyMatching which honors the
// partition list instead. This probe lets agentcookie's wizard tell which
// access strategy actually works for kooky callers.
package chrome

import (
	"context"
	"errors"
	"fmt"
	"time"

	keychain "github.com/keybase/go-keychain"
)

// ErrProbeTimedOut means the Keychain call did not return within the
// probe's deadline. The most common cause is a SecurityAgent prompt sitting
// on the user's screen waiting for an Always-Allow / Deny click. From a
// LaunchAgent context with the right ACL/partition setup, the call returns
// in milliseconds.
var ErrProbeTimedOut = errors.New("probe: Keychain call exceeded deadline (likely a SecurityAgent prompt is hung waiting for a click)")

// KeybaseKeychainProbe calls keychain.GetGenericPassword for the Chrome
// Safe Storage item, with a hard timeout. Returns the password byte
// length on success (never the password itself -- avoid leaking it through
// stderr or process output).
//
// timeout caps how long we wait for the Keychain API. A hung call almost
// always means a prompt is pending; treat that as a typed failure so the
// wizard's strategy loop can move on without burning ~30s per attempt.
func KeybaseKeychainProbe(timeout time.Duration) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)

	go func() {
		password, err := keychain.GetGenericPassword(keychainService, keychainAccount, "", "")
		if err != nil {
			ch <- result{0, fmt.Errorf("keybase keychain GetGenericPassword: %w", err)}
			return
		}
		ch <- result{len(password), nil}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case r := <-ch:
		return r.n, r.err
	case <-ctx.Done():
		return 0, ErrProbeTimedOut
	}
}
