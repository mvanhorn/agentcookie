package storagecodec

import "testing"

func TestValidOrigin(t *testing.T) {
	for _, s := range []string{"https://example.com", "http://localhost:18888", "http://[::1]:1234"} {
		if err := ValidOrigin(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"file:///tmp", "https://user:secret@example.com", "https://example.com/path", "https://example.com?x", "https://example.com#x", "javascript:alert(1)", "https://"} {
		if ValidOrigin(s) == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
