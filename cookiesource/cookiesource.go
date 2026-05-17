// Package cookiesource is the importable PP-CLI integration point for the
// agentcookie bridge. PP CLIs that read Chrome cookies via kooky (or any
// other Chrome-cookies library) call cookiesource.Open to discover the
// path they should read from. When agentcookie is installed and the
// AGENTCOOKIE_PLAIN_COOKIES env var is set, Open returns the sidecar
// path with PlainText=true. When not, Open returns the empty default
// and PlainText=false, signaling the caller to fall back to its existing
// kooky-discovery flow.
//
// The sidecar file is a plaintext Chrome-shaped SQLite at
// ~/.agentcookie/cookies-plain.db. PP CLIs reading it skip both the
// macOS Keychain handshake and Chrome 127+'s App-Bound encryption.
//
// Typical PP CLI integration:
//
//	src, err := cookiesource.Open()
//	if err != nil {
//	    // ...
//	}
//	if src.PlainText {
//	    cookies = readSidecar(src.Path)
//	} else {
//	    cookies = kooky.FindAllCookieStores() // existing fallback
//	}
//
// Or the one-line version via ReadCookies, which handles both paths
// internally.
package cookiesource

import (
	"errors"
	"fmt"
	"os"
)

// EnvVar is the environment variable PP CLIs and Hermes-style agents
// set on a Mac mini to opt in to the agentcookie bridge. Its value is
// the absolute path to the plaintext sidecar SQLite.
const EnvVar = "AGENTCOOKIE_PLAIN_COOKIES"

// ErrSidecarMissing is returned by Open when AGENTCOOKIE_PLAIN_COOKIES
// is set but the file does not exist on disk. Callers can treat this as
// "agentcookie not installed (or not yet synced)" and fall back to
// their default Chrome path.
var ErrSidecarMissing = errors.New("cookiesource: AGENTCOOKIE_PLAIN_COOKIES is set but the file does not exist (is agentcookie installed and has it run at least one sync?)")

// Source describes the cookie store the PP CLI should read from.
//
// PlainText=true means the caller should read Path as a Chrome-shaped
// SQLite whose `value` column holds plaintext cookies and whose
// `encrypted_value` column is empty. No Keychain access required.
//
// PlainText=false (and empty Path) means the caller should fall back to
// its existing kooky-discovery or hardcoded-Chrome-path flow.
type Source struct {
	Path      string
	PlainText bool
}

// Open returns the Source the PP CLI should read cookies from.
// Honors AGENTCOOKIE_PLAIN_COOKIES; falls back when unset.
//
// Returns ErrSidecarMissing if the env var is set but the file does
// not exist; callers usually want to log and continue with fallback.
func Open() (Source, error) {
	path := os.Getenv(EnvVar)
	if path == "" {
		return Source{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Source{Path: path, PlainText: true}, ErrSidecarMissing
		}
		return Source{}, fmt.Errorf("cookiesource: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return Source{}, fmt.Errorf("cookiesource: %s is a directory, not a SQLite file", path)
	}
	return Source{Path: path, PlainText: true}, nil
}

// Available reports whether the agentcookie bridge is configured AND the
// sidecar file exists. Cheap probe for tooling that wants to log
// "using agentcookie bridge" without surfacing errors. Returns false
// (with no error) for any reason the bridge is not usable.
func Available() bool {
	src, err := Open()
	return err == nil && src.PlainText && src.Path != ""
}
