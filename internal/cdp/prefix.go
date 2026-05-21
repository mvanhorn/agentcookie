// Package cdp implements Chrome DevTools Protocol cookie injection
// against the agentcookie-owned Chrome profile. This is the
// v0.12.0-beta.3 path that lets a headless sink keep Chrome's SQLite
// warm without agentcookie ever reading Chrome's Safe Storage Keychain
// item.
//
// Note on the Chrome 127+ App-Bound Encryption 32-byte prefix: the
// source side (internal/chrome/read.go) already strips the prefix
// defensively (using SHA256(host_key) match) before shipping. CDP
// callers receive already-stripped values and must NOT double-strip.
// v0.12.0-beta.3 added an unconditional strip here that mangled real
// cookie values; v0.12.0-beta.4 removed it. See the 2026-05-21
// dry-run friction item #21.
package cdp
