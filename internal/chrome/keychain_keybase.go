//go:build darwin && cgo

package chrome

import (
	"context"
	"errors"
	"time"

	keychain "github.com/keybase/go-keychain"
)

// keybaseReadTimeout caps how long safeStoragePasswordViaKeybase will
// wait for the modern Apple Security API (SecItemCopyMatching) before
// giving up and letting the caller fall through to the security CLI
// path. Three seconds is well past the call's normal success time
// (sub-millisecond when the keychain is unlocked and the binary is in
// the trust list) but well under the user's tolerance for "is it
// hung?" -- if the keybase path is going to prompt, we want to abandon
// it and let the legacy CLI succeed instead of blocking forever on a
// SecurityAgent prompt nobody is going to answer.
const keybaseReadTimeout = 3 * time.Second

// ErrKeybaseTimedOut means the keybase/go-keychain call did not return
// within keybaseReadTimeout. Almost always indicates a hung
// SecurityAgent prompt: the modern SecItemCopyMatching API requires the
// calling binary to be in the keychain item's per-app trust list AND
// signed by a recognized identity. Ad-hoc-signed Go binaries
// (`go install` output) fail both conditions, so SecItem blocks
// waiting for a UI prompt that no one is going to dismiss.
var ErrKeybaseTimedOut = errors.New("keybase keychain GetGenericPassword timed out (likely a SecurityAgent prompt waiting that no one can dismiss; fall back to the security CLI)")

// safeStoragePasswordViaKeybase reads Chrome Safe Storage via the
// modern SecItem API path. Used as the fast path on darwin+CGO builds;
// callers fall back to the security CLI if this returns an error.
//
// The CGO call is run in a goroutine and capped by keybaseReadTimeout
// so a hung SecurityAgent prompt does not stall the entire process.
// In practice this happens often on Mac mini sinks where agentcookie
// is the only ad-hoc-signed binary the user re-deploys frequently --
// each rebuild invalidates the cdhash that the per-app trust list
// pinned, so subsequent SecItem calls hit a prompt that no one will
// answer.
func safeStoragePasswordViaKeybase() (string, error) {
	type result struct {
		pw  []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		password, err := keychain.GetGenericPassword(keychainService, keychainAccount, "", "")
		ch <- result{pw: password, err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), keybaseReadTimeout)
	defer cancel()
	select {
	case r := <-ch:
		if r.err != nil {
			return "", r.err
		}
		return string(r.pw), nil
	case <-ctx.Done():
		return "", ErrKeybaseTimedOut
	}
}
