package cli

import (
	"encoding/json"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/chrome"
)

func TestToExportCookies(t *testing.T) {
	persistentExpiry := int64(1800000000) // unix seconds
	// Chrome stores expiry as microseconds since 1601.
	chromeExpiry := (persistentExpiry + exportEpochOffsetSec) * 1_000_000

	cookies := []chrome.Cookie{
		{
			HostKey:    ".github.com",
			Name:       "__Host-user_session_same_site",
			Value:      "tok",
			Path:       "/",
			IsSecure:   1,
			IsHTTPOnly: 1,
			SameSite:   1, // lax
			HasExpires: 1,
			ExpiresUTC: chromeExpiry,
		},
		{
			HostKey:    "github.com",
			Name:       "_gh_sess",
			Value:      "sess",
			Path:       "/",
			IsSecure:   1,
			IsHTTPOnly: 1,
			SameSite:   0,  // None -> no_restriction
			HasExpires: 0,  // session cookie -> no expirationDate
			ExpiresUTC: 99, // ignored because HasExpires == 0
		},
	}

	out := toExportCookies(cookies)
	if len(out) != 2 {
		t.Fatalf("expected 2 cookies, got %d", len(out))
	}

	host := out[0]
	if host.Domain != ".github.com" || host.Name != "__Host-user_session_same_site" {
		t.Fatalf("unexpected first cookie: %+v", host)
	}
	if !host.Secure || !host.HTTPOnly || host.SameSite != "lax" {
		t.Fatalf("flags/sameSite mismapped: %+v", host)
	}
	if host.ExpirationDate == nil || *host.ExpirationDate != persistentExpiry {
		t.Fatalf("expirationDate want %d, got %v", persistentExpiry, host.ExpirationDate)
	}

	sess := out[1]
	if sess.SameSite != "no_restriction" {
		t.Fatalf("None should map to no_restriction, got %q", sess.SameSite)
	}
	if sess.ExpirationDate != nil {
		t.Fatalf("session cookie must omit expirationDate, got %v", *sess.ExpirationDate)
	}

	// The output must be a JSON array of objects with the field names orca's
	// importer reads, so `agentcookie export | orca cookie import` needs no
	// mapping step on the consumer side.
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundtrip []map[string]any
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	first := roundtrip[0]
	for _, field := range []string{"domain", "name", "value", "path", "secure", "httpOnly", "sameSite", "expirationDate"} {
		if _, ok := first[field]; !ok {
			t.Fatalf("missing field %q in export JSON: %v", field, first)
		}
	}
	if _, ok := roundtrip[1]["expirationDate"]; ok {
		t.Fatalf("session cookie should omit expirationDate in JSON, got %v", roundtrip[1])
	}
}

func TestExportSameSite(t *testing.T) {
	cases := map[int]string{0: "no_restriction", 1: "lax", 2: "strict", -1: "unspecified", 99: "unspecified"}
	for in, want := range cases {
		if got := exportSameSite(in); got != want {
			t.Fatalf("exportSameSite(%d) = %q, want %q", in, got, want)
		}
	}
}
