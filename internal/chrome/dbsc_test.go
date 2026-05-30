package chrome

import (
	"strings"
	"testing"
	"time"
)

// chromeMicros converts a wall-clock time to Chrome's expires_utc form
// (microseconds since 1601-01-01 UTC) so tests can build cookies with a
// known remaining TTL relative to a fixed "now".
func chromeMicros(t time.Time) int64 {
	return t.UnixMicro() + chromeEpochOffsetMicros
}

func TestClassifyDBSC(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		cookie   Cookie
		skip     bool
		want     DBSCDecision
		reasonIs string // substring the reason must contain ("" = expect empty reason)
	}{
		{
			name: "normal long-lived secure non-DBSC cookie ships clean",
			cookie: Cookie{
				HostKey: ".instacart.com", Name: "session", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(720 * time.Hour)),
			},
			want:     DBSCShip,
			reasonIs: "",
		},
		{
			name: "secure cookie on known Google host is suspect regardless of TTL",
			cookie: Cookie{
				HostKey: ".google.com", Name: "SID", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(720 * time.Hour)),
			},
			want:     DBSCShipWarn,
			reasonIs: "DBSC",
		},
		{
			name: "short-TTL secure cookie on non-allowlisted host warns but never skips by default",
			cookie: Cookie{
				HostKey: ".example.com", Name: "auth", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(7 * time.Minute)),
			},
			want:     DBSCShipWarn,
			reasonIs: "expires within",
		},
		{
			name: "short-TTL non-secure cookie is not flagged",
			cookie: Cookie{
				HostKey: ".example.com", Name: "prefs", IsSecure: 0,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(7 * time.Minute)),
			},
			want:     DBSCShip,
			reasonIs: "",
		},
		{
			name: "already-expired secure cookie is not flagged (pipeline handles expiry)",
			cookie: Cookie{
				HostKey: ".example.com", Name: "auth", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(-5 * time.Minute)),
			},
			want:     DBSCShip,
			reasonIs: "",
		},
		{
			name: "session cookie (no expiry) is not flagged as short-TTL",
			cookie: Cookie{
				HostKey: ".example.com", Name: "auth", IsSecure: 1,
				HasExpires: 0, IsPersistent: 0, ExpiresUTC: 0,
			},
			want:     DBSCShip,
			reasonIs: "",
		},
		{
			name: "skip mode turns a suspect into Skip with a reason",
			cookie: Cookie{
				HostKey: ".example.com", Name: "auth", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(7 * time.Minute)),
			},
			skip:     true,
			want:     DBSCSkip,
			reasonIs: "expires within",
		},
		{
			name: "skip mode leaves a clean cookie untouched",
			cookie: Cookie{
				HostKey: ".instacart.com", Name: "session", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(720 * time.Hour)),
			},
			skip:     true,
			want:     DBSCShip,
			reasonIs: "",
		},
		{
			name: "known-host match is suffix-based and case-insensitive",
			cookie: Cookie{
				HostKey: "ACCOUNTS.GOOGLE.COM", Name: "__Secure-1PSID", IsSecure: 1,
				HasExpires: 1, IsPersistent: 1,
				ExpiresUTC: chromeMicros(now.Add(720 * time.Hour)),
			},
			want:     DBSCShipWarn,
			reasonIs: "DBSC",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ClassifyDBSC(tc.cookie, now, tc.skip)
			if got != tc.want {
				t.Fatalf("ClassifyDBSC = %v, want %v (reason=%q)", got, tc.want, reason)
			}
			if tc.reasonIs == "" {
				if reason != "" {
					t.Fatalf("expected empty reason, got %q", reason)
				}
			} else if !strings.Contains(reason, tc.reasonIs) {
				t.Fatalf("reason %q does not contain %q", reason, tc.reasonIs)
			}
		})
	}
}

func TestClassifyCookies(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	cookies := []Cookie{
		{HostKey: ".instacart.com", Name: "session", IsSecure: 1, HasExpires: 1, IsPersistent: 1, ExpiresUTC: chromeMicros(now.Add(720 * time.Hour))},
		{HostKey: ".google.com", Name: "SID", IsSecure: 1, HasExpires: 1, IsPersistent: 1, ExpiresUTC: chromeMicros(now.Add(720 * time.Hour))},
		{HostKey: ".example.com", Name: "auth", IsSecure: 1, HasExpires: 1, IsPersistent: 1, ExpiresUTC: chromeMicros(now.Add(7 * time.Minute))},
	}

	t.Run("warn mode ships everything and records two warnings", func(t *testing.T) {
		res := ClassifyCookies(cookies, now, false)
		if len(res.Shipped) != 3 {
			t.Fatalf("warn mode should ship all 3, shipped %d", len(res.Shipped))
		}
		if len(res.Warned) != 2 {
			t.Fatalf("expected 2 warnings, got %d: %v", len(res.Warned), res.Warned)
		}
		if len(res.Skipped) != 0 {
			t.Fatalf("warn mode must never skip, skipped %d", len(res.Skipped))
		}
	})

	t.Run("skip mode drops the two suspects", func(t *testing.T) {
		res := ClassifyCookies(cookies, now, true)
		if len(res.Shipped) != 1 {
			t.Fatalf("skip mode should ship only the clean cookie, shipped %d", len(res.Shipped))
		}
		if res.Shipped[0].HostKey != ".instacart.com" {
			t.Fatalf("wrong cookie survived: %s", res.Shipped[0].HostKey)
		}
		if len(res.Skipped) != 2 {
			t.Fatalf("expected 2 skips, got %d", len(res.Skipped))
		}
	})
}
