package cdp

import (
	"bytes"
	"strings"
	"testing"
)

// TestStripAppBoundPrefix exercises the table of edge cases the
// project memory at reference_chrome_app_bound_encryption.md
// documents. The prefix is exactly 32 bytes; shorter inputs pass
// through unchanged; exactly-prefix-long inputs produce empty
// outputs; longer inputs return the suffix after the 32-byte mark.
func TestStripAppBoundPrefix(t *testing.T) {
	prefix := bytes.Repeat([]byte{0xAB}, appBoundPrefixLen)

	t.Run("32-byte prefix + ASCII tail", func(t *testing.T) {
		tail := []byte("abc123")
		got := StripAppBoundPrefix(append(prefix, tail...))
		if !bytes.Equal(got, tail) {
			t.Errorf("got %q, want %q", got, tail)
		}
	})

	t.Run("32-byte prefix + binary tail", func(t *testing.T) {
		tail := []byte{0x00, 0x01, 0x02, 0xFF, 0x7F}
		got := StripAppBoundPrefix(append(prefix, tail...))
		if !bytes.Equal(got, tail) {
			t.Errorf("binary tail mangled: got %v, want %v", got, tail)
		}
	})

	t.Run("exactly 32 bytes returns empty", func(t *testing.T) {
		got := StripAppBoundPrefix(prefix)
		if len(got) != 0 {
			t.Errorf("exact-prefix-only input should return empty, got %d bytes", len(got))
		}
	})

	t.Run("16-byte input passes through unchanged", func(t *testing.T) {
		// Likely a legacy v11 cookie or non-v10-encrypted value. We
		// must not corrupt it.
		shorter := []byte("legacy16byteval!")
		got := StripAppBoundPrefix(shorter)
		if !bytes.Equal(got, shorter) {
			t.Errorf("short input mangled: got %q, want %q", got, shorter)
		}
	})

	t.Run("empty input returns empty", func(t *testing.T) {
		got := StripAppBoundPrefix(nil)
		if len(got) != 0 {
			t.Errorf("empty input should return empty, got %d bytes", len(got))
		}
	})

	t.Run("idempotent within same host_key", func(t *testing.T) {
		// Per the memory: cookies sharing a host_key all use the
		// same 32-byte prefix. Stripping each one drops the same
		// 32 bytes; the strip is per-cookie, not per-host.
		host := bytes.Repeat([]byte{0x42}, appBoundPrefixLen)
		v1 := append(append([]byte{}, host...), []byte("session=abc")...)
		v2 := append(append([]byte{}, host...), []byte("refresh=xyz")...)
		if !strings.HasPrefix(string(StripAppBoundPrefix(v1)), "session=") {
			t.Errorf("v1 strip failed")
		}
		if !strings.HasPrefix(string(StripAppBoundPrefix(v2)), "refresh=") {
			t.Errorf("v2 strip failed")
		}
	})
}
