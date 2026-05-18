package sinkpush

// init registers the built-in adapters at package-load time. Importing
// this package once anywhere in the binary (typically internal/cli/sink.go)
// makes all five initial adapters available to RunAll.
//
// Adapters are registered in their plan-ordered sequence:
//   1. instacart-pp-cli (U2; auth-paste strategy)
//   2. airbnb-pp-cli (U3; pycookiecheat-style TOML+JSON)
//   3. ebay-pp-cli (U3; same)
//   4. pagliacci-pp-cli (U3; same; unverified end-to-end)
//   5. table-reservation-goat-pp-cli (U4; single session.json)
//
// Tests that need a clean registry call resetForTesting() first, then
// Register their stubs. The built-ins re-register only on a fresh
// package load (next test binary), not between individual tests.
func init() {
	Register(NewInstacart())
	Register(NewAirbnb())
	Register(NewEbay())
	Register(NewPagliacci())
	Register(NewTableReservation())
}
