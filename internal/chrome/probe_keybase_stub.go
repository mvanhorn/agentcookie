//go:build !darwin || !cgo

package chrome

import (
	"errors"
	"time"
)

// ErrProbeTimedOut keeps a build-friendly identifier across non-darwin or
// no-CGO build configurations. See probe_keybase.go for the real implementation.
var ErrProbeTimedOut = errors.New("probe: Keychain call exceeded deadline (not applicable on this build)")

// KeybaseKeychainProbe stubs out on non-darwin and no-CGO builds. The
// real implementation needs both darwin and CGO (the keybase library is
// a CGO wrapper around macOS Security framework).
func KeybaseKeychainProbe(timeout time.Duration) (int, error) {
	return 0, errors.New("KeybaseKeychainProbe: requires darwin + CGO build")
}
