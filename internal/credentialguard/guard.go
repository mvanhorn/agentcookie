// Package credentialguard supplies an explicit fail-closed test boundary.
// Production never enables it. End-to-end tests enable it before assembling
// readers or sinks, so a missing dependency injection panics before host I/O.
package credentialguard

import "sync/atomic"

var forbidden atomic.Int64

func ForbidRealProviders() func() { forbidden.Add(1); return func() { forbidden.Add(-1) } }
func Check(provider string) {
	if forbidden.Load() > 0 {
		panic("hermetic test invoked forbidden provider: " + provider)
	}
}
