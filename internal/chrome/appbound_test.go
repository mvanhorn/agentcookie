package chrome

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestStripAppBoundPrefix_StripsCorrectHost(t *testing.T) {
	host := ".github.com"
	value := "actual-cookie-value"
	prefix := sha256.Sum256([]byte(host))
	plaintext := append(prefix[:], value...)

	got := stripAppBoundPrefix(plaintext, host)
	if string(got) != value {
		t.Errorf("expected %q, got %q", value, string(got))
	}
}

func TestStripAppBoundPrefix_PreservesPlaintextWhenHostMismatches(t *testing.T) {
	// Plaintext claims to be for host_key A but bears SHA256(B) prefix.
	// Defense: do not strip. Return as-is so the cookie either round-trips
	// unchanged (sink can detect the mismatch later) or surfaces as
	// malformed at the destination.
	host := "github.com"
	otherPrefix := sha256.Sum256([]byte("example.com"))
	value := "actual-cookie-value"
	plaintext := append(otherPrefix[:], value...)

	got := stripAppBoundPrefix(plaintext, host)
	if !bytes.Equal(got, plaintext) {
		t.Error("expected plaintext to pass through unchanged when prefix mismatches host")
	}
}

func TestStripAppBoundPrefix_PassesThroughPre127Plaintext(t *testing.T) {
	// Pre-127 Chrome: no prefix at all. Plaintext is just the cookie value.
	host := ".github.com"
	value := "actual-cookie-value"
	plaintext := []byte(value)

	got := stripAppBoundPrefix(plaintext, host)
	if !bytes.Equal(got, plaintext) {
		t.Errorf("expected plaintext to pass through unchanged, got %q", string(got))
	}
}

func TestStripAppBoundPrefix_ShortPlaintextUnchanged(t *testing.T) {
	// Plaintext shorter than 32 bytes cannot have a prefix.
	got := stripAppBoundPrefix([]byte("short"), ".github.com")
	if string(got) != "short" {
		t.Errorf("expected short plaintext unchanged, got %q", string(got))
	}
}

func TestStripAppBoundPrefix_EmptyPlaintext(t *testing.T) {
	got := stripAppBoundPrefix(nil, ".github.com")
	if len(got) != 0 {
		t.Errorf("expected empty plaintext, got %v", got)
	}
}

func TestStripAppBoundPrefix_DifferentHostsProduceDifferentResults(t *testing.T) {
	// Same value bytes, different host_keys: stripping with the wrong host
	// returns unchanged plaintext (no false-positive strip).
	valueBytes := []byte("payload")
	prefixGithub := sha256.Sum256([]byte("github.com"))
	plaintext := append(prefixGithub[:], valueBytes...)

	rightHost := stripAppBoundPrefix(plaintext, "github.com")
	if !bytes.Equal(rightHost, valueBytes) {
		t.Error("right host_key did not strip prefix")
	}

	wrongHost := stripAppBoundPrefix(plaintext, "github.io")
	if !bytes.Equal(wrongHost, plaintext) {
		t.Error("wrong host_key falsely stripped prefix")
	}
}

func TestStripAppBoundPrefix_HandlesValueThatLooksLikePrefix(t *testing.T) {
	// Edge case: a cookie whose actual value happens to start with 32 random
	// bytes that don't match SHA256(host_key). We must not strip them.
	host := ".github.com"
	value := append([]byte{0xde, 0xad, 0xbe, 0xef}, bytes.Repeat([]byte{0x42}, 28)...)
	value = append(value, []byte("trailing")...)

	got := stripAppBoundPrefix(value, host)
	if !bytes.Equal(got, value) {
		t.Error("plaintext that does not match SHA256(host_key) prefix was incorrectly stripped")
	}
}
