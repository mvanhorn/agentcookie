package gecko

import "testing"

// SameSite=None without Secure is what Chrome rejects and Firefox loads as
// unset, so it maps to Chrome's unspecified; with Secure it stays None.
func TestReadCookiesSameSiteNoneRequiresSecure(t *testing.T) {
	for _, tc := range []struct {
		version int
		expiry  int64
	}{
		{14, realExpiryMS / 1000},
		{17, realExpiryMS},
	} {
		insecure := row("insecure", ".example.com", tc.expiry)
		insecure.sameSite, insecure.rawSameSite = 0, 0
		secure := insecure
		secure.name, secure.isSecure = "secure", 1
		got, err := ReadCookies(writeFixture(t, tc.version, []mozRow{insecure, secure}), fixtureNow)
		if err != nil {
			t.Fatalf("v%d: ReadCookies: %v", tc.version, err)
		}
		m := byName(t, got)
		if c := m["insecure"]; c.SameSite != chromeSameSiteUnspecified || c.IsSecure != 0 {
			t.Errorf("v%d insecure None: SameSite = %d, IsSecure = %d; want %d, 0", tc.version, c.SameSite, c.IsSecure, chromeSameSiteUnspecified)
		}
		if c := m["secure"]; c.SameSite != chromeSameSiteNone || c.IsSecure != 1 {
			t.Errorf("v%d secure None: SameSite = %d, IsSecure = %d; want %d, 1", tc.version, c.SameSite, c.IsSecure, chromeSameSiteNone)
		}
	}
}
