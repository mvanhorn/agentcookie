package gecko

import (
	"testing"
	"time"
)

// Schema 15 dropped rawSameSite but still stores expiry in seconds; schema
// 16 switched expiry to milliseconds. Both must give the same Chrome expiry
// and drop expired rows.
func TestReadCookiesSchema15And16Expiry(t *testing.T) {
	for _, tc := range []struct {
		version int
		scale   int64 // expiry units per second
	}{
		{15, 1},
		{16, 1000},
	} {
		live := row("live", ".example.com", realExpiryMS/1000*tc.scale)
		// On schema 15+ a stored Lax is the real attribute, not a default.
		live.sameSite = mozSameSiteLax
		old := row("old", ".example.com", fixtureNow.Add(-time.Minute).Unix()*tc.scale)
		got, err := ReadCookies(writeFixture(t, tc.version, []mozRow{live, old}), fixtureNow)
		if err != nil {
			t.Fatalf("v%d: ReadCookies: %v", tc.version, err)
		}
		if len(got) != 1 || got[0].Name != "live" {
			t.Fatalf("v%d: got %v, want only the unexpired cookie", tc.version, byName(t, got))
		}
		if want := chromeMicros(realExpiryMS / 1000); got[0].ExpiresUTC != want {
			t.Errorf("v%d: ExpiresUTC = %d, want %d", tc.version, got[0].ExpiresUTC, want)
		}
		if got[0].SameSite != chromeSameSiteLax {
			t.Errorf("v%d: SameSite = %d, want Lax", tc.version, got[0].SameSite)
		}
	}
}
