//go:build !macapp

package macapp

// embeddedZip is empty in builds without the macapp tag (development builds,
// Linux): services run the binary directly and doctor says so on macOS.
var embeddedZip []byte
