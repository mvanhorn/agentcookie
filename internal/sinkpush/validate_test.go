package sinkpush

import (
	"errors"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestValidate_AcceptsHealthyCookies(t *testing.T) {
	cases := []chrome.Cookie{
		{Name: "session", Value: "abc123", HostKey: "instacart.com"},
		{Name: "_ga", Value: "GA1.2.123.456", HostKey: ".instacart.com"},
		{Name: "cf_clearance", Value: "abc.def-ghi_jkl=", HostKey: "instacart.com"},
		{Name: "cookie-name_with-dash", Value: "v", HostKey: "ebay.com"},
		// Large valid value (base64-ish) is fine
		{Name: "JWT", Value: strings.Repeat("a", 4096), HostKey: "airbnb.com"},
		// Extended-ASCII value byte is rejected by the strict checker;
		// values stay in 0x20-0xFF range conservatively.
		{Name: "x", Value: "v\xC2\xA9", HostKey: "ebay.com"}, // U+00A9 in UTF-8
	}
	for _, c := range cases {
		if err := Validate(c); err != nil {
			t.Errorf("Validate(%q=...) returned %v, want nil", c.Name, err)
		}
	}
}

func TestValidate_RejectsBadName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr error
	}{
		{"", errEmptyName},
		{"name with space", errNameTokenChars},
		{"with;semicolon", errNameTokenChars},
		{"with=equals", errNameTokenChars},
		{"with\nnewline", errNameTokenChars},
		{"with\x00nul", errNameTokenChars},
		{"with/slash", errNameTokenChars},
		{"with\"quote", errNameTokenChars},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(chrome.Cookie{Name: tc.name, Value: "v", HostKey: "example.com"})
			if err == nil {
				t.Fatalf("Validate name=%q returned nil, want %v", tc.name, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate name=%q returned %v, want %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func TestValidate_RejectsControlCharsInValue(t *testing.T) {
	cases := []string{
		"v\x00alue",
		"v\r\nadmin=true",
		"v\talue",
		"v\x7Falue",
		"v\x1Falue",
	}
	for _, v := range cases {
		err := Validate(chrome.Cookie{Name: "x", Value: v, HostKey: "example.com"})
		if !errors.Is(err, errValueControl) {
			t.Errorf("Validate value=%q returned %v, want %v", v, err, errValueControl)
		}
	}
}

func TestValidate_RejectsBadHostKey(t *testing.T) {
	cases := []struct {
		hostKey string
		wantErr error
	}{
		{"", errHostKeyEmpty},
		{"foo\x00bar.com", errHostKeyControl},
		{"foo/bar.com", errHostKeyTraverse},
		{"foo\\bar.com", errHostKeyTraverse},
		{"foo..bar.com", errHostKeyTraverse},
	}
	for _, tc := range cases {
		t.Run(tc.hostKey, func(t *testing.T) {
			err := Validate(chrome.Cookie{Name: "x", Value: "v", HostKey: tc.hostKey})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate hostKey=%q returned %v, want %v", tc.hostKey, err, tc.wantErr)
			}
		})
	}
}

func TestHostSuffixMatch(t *testing.T) {
	cases := []struct {
		hostKey string
		suffix  string
		want    bool
	}{
		{"opentable.com", "opentable.com", true},
		{".opentable.com", "opentable.com", true},
		{"www.opentable.com", "opentable.com", true},
		{"sub.www.opentable.com", "opentable.com", true},
		{"xopentable.com", "opentable.com", false},
		{"opentable.com.evil.com", "opentable.com", false},
		{"opentable.co", "opentable.com", false},
		{"", "opentable.com", false},
		{"opentable.com", "", false},
		{"opentable.com", "OPENTABLE.COM", false}, // case-sensitive
	}
	for _, tc := range cases {
		got := HostSuffixMatch(tc.hostKey, tc.suffix)
		if got != tc.want {
			t.Errorf("HostSuffixMatch(%q, %q) = %v, want %v", tc.hostKey, tc.suffix, got, tc.want)
		}
	}
}
