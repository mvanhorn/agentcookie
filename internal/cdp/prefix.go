// Package cdp implements Chrome DevTools Protocol cookie injection
// against the agentcookie-owned Chrome profile. This is the
// v0.12.0-beta.3 path that lets a headless sink keep Chrome's SQLite
// warm without agentcookie ever reading Chrome's Safe Storage Keychain
// item.
package cdp

// appBoundPrefixLen is the length of the host-key-derived prefix that
// Chrome 127+ on macOS prepends to decrypted cookie plaintext. See
// the project memory at reference_chrome_app_bound_encryption.md:
// decrypted v10 plaintext is now `<32-byte-host-bound-prefix> ||
// <actual-cookie-value>`. The prefix is identical across cookies
// sharing the same host_key but differs between hosts. The SQLite
// re-encrypt path round-trips the full plaintext (prefix included),
// so Chrome strips the prefix on its own read. The CDP path wants
// only the actual cookie value as a UTF-8 string, so we strip here
// before calling Storage.setCookies.
const appBoundPrefixLen = 32

// StripAppBoundPrefix removes the 32-byte host-key-derived prefix from
// a v10-decrypted Chrome cookie value. Inputs shorter than the prefix
// length are returned unchanged (legacy v11 cookies or empty values).
// Inputs exactly the prefix length return an empty value (a v10
// cookie with an empty actual value, which is valid in Chrome's
// format).
func StripAppBoundPrefix(value []byte) []byte {
	if len(value) < appBoundPrefixLen {
		return value
	}
	return value[appBoundPrefixLen:]
}
