package sinkpush

import (
	"errors"
	"fmt"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

// Validate reports whether a cookie's Name, Value, and HostKey are safe
// to hand to an adapter writer. Adapters write Name=Value pairs into
// downstream files (TOML configs, JSON sessions, header-string stdin
// for "auth paste") whose parsers can be confused by control characters
// or unescaped quoting. The validator is the single gate that prevents
// a compromised source from smuggling additional fields or breaking the
// downstream file shape.
//
// Validate is intentionally strict on Name (RFC 6265 token chars) and
// permissive on Value (any non-control char is allowed). HostKey must
// be a syntactically plausible hostname-with-optional-leading-dot;
// path-traversal characters and embedded NULs are rejected.
//
// Returns nil when the cookie is safe to use. Returns a typed error
// when it isn't; callers should drop the cookie and increment the
// Result.Invalid counter rather than aborting.
func Validate(c chrome.Cookie) error {
	if err := validateName(c.Name); err != nil {
		return fmt.Errorf("name %q: %w", c.Name, err)
	}
	if err := validateValue(c.Value); err != nil {
		return fmt.Errorf("value for %q: %w", c.Name, err)
	}
	if err := validateHostKey(c.HostKey); err != nil {
		return fmt.Errorf("host_key %q: %w", c.HostKey, err)
	}
	return nil
}

// Reasons returned by Validate so tests and callers can match on them
// without parsing message strings.
var (
	errEmptyName       = errors.New("empty")
	errNameTokenChars  = errors.New("contains non-token characters")
	errValueControl    = errors.New("contains control character")
	errHostKeyEmpty    = errors.New("empty")
	errHostKeyControl  = errors.New("contains control character")
	errHostKeyTraverse = errors.New("contains path-traversal characters")
)

// validateName: RFC 6265 cookie-name = token. token chars are any CHAR
// except CTLs and separators. We accept the conservative subset most
// production cookies use: ASCII alphanumeric plus `!#$%&'*+-.^_` `|~`.
func validateName(name string) error {
	if name == "" {
		return errEmptyName
	}
	for _, r := range name {
		if r > 0x7E || r < 0x21 {
			return errNameTokenChars
		}
		// RFC 6265 separator list: ( ) < > @ , ; : \ " / [ ] ? = { }
		switch r {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"',
			'/', '[', ']', '?', '=', '{', '}':
			return errNameTokenChars
		}
	}
	return nil
}

// validateValue: cookie values can contain a wide range of characters
// (URL-encoded JSON tokens, base64, etc.). We reject only ASCII control
// characters (0x00-0x1F and 0x7F) which are the bytes that confuse
// downstream parsers when written into a Cookie header or TOML file.
func validateValue(value string) error {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 || b == 0x7F {
			return errValueControl
		}
	}
	return nil
}

// validateHostKey: Chrome's host_key column is either a bare hostname
// (`opentable.com`) or a hostname with a leading dot for subdomain
// cookies (`.opentable.com`). We reject control characters, embedded
// NULs, and path-traversal characters that could escape an
// adapter-written directory.
func validateHostKey(hostKey string) error {
	if hostKey == "" {
		return errHostKeyEmpty
	}
	for i := 0; i < len(hostKey); i++ {
		b := hostKey[i]
		if b < 0x20 || b == 0x7F {
			return errHostKeyControl
		}
	}
	if strings.Contains(hostKey, "..") ||
		strings.Contains(hostKey, "/") ||
		strings.Contains(hostKey, "\\") {
		return errHostKeyTraverse
	}
	return nil
}

// HostSuffixMatch reports whether hostKey ends with suffix on a
// label boundary. `opentable.com` and `.opentable.com` both match the
// suffix `opentable.com`; `xopentable.com` does not. Adapters use this
// to bin cookies between sibling services (OpenTable vs Tock under
// the table-reservation adapter) without the bug of a string-suffix
// match accidentally matching unrelated hosts.
func HostSuffixMatch(hostKey, suffix string) bool {
	if suffix == "" || hostKey == "" {
		return false
	}
	if hostKey == suffix {
		return true
	}
	if !strings.HasSuffix(hostKey, suffix) {
		return false
	}
	// hostKey is longer than suffix here; the character immediately
	// before the suffix must be the label separator `.`.
	boundary := hostKey[len(hostKey)-len(suffix)-1]
	return boundary == '.'
}
