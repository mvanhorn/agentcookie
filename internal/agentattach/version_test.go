package agentattach

import "testing"

func TestParseChromeMajor(t *testing.T) {
	cases := []struct {
		in      string
		wantMaj int
		wantOK  bool
	}{
		{"Chrome/148.0.7778.217", 148, true},
		{"HeadlessChrome/120.0.6099.71", 120, true},
		{"148.0.7778.217", 148, true}, // bare version, no product prefix
		{"Chrome/136.0.6778.85", 136, true},
		{"  Chrome/144.0.1.2  ", 144, true}, // surrounding whitespace
		{"", 0, false},
		{"Chrome/", 0, false},
		{"Chrome/abc.0", 0, false},
		{"garbage", 0, false},
		{"Chrome/0.0.0.0", 0, false}, // zero major is not valid
	}
	for _, c := range cases {
		gotMaj, gotOK := ParseChromeMajor(c.in)
		if gotMaj != c.wantMaj || gotOK != c.wantOK {
			t.Errorf("ParseChromeMajor(%q) = (%d, %v), want (%d, %v)", c.in, gotMaj, gotOK, c.wantMaj, c.wantOK)
		}
	}
}

func TestTierForVersion(t *testing.T) {
	cases := []struct {
		major int
		want  PolicyTier
	}{
		{148, TierAutoConnect},
		{144, TierAutoConnect},
		{143, TierCustomDirOnly},
		{136, TierCustomDirOnly},
		{135, TierLegacy},
		{120, TierLegacy},
		{1, TierLegacy},
		{0, TierUnknown},
		{-1, TierUnknown},
	}
	for _, c := range cases {
		if got := TierForVersion(c.major); got != c.want {
			t.Errorf("TierForVersion(%d) = %v, want %v", c.major, got, c.want)
		}
	}
}

func TestPolicyTierString(t *testing.T) {
	cases := map[PolicyTier]string{
		TierLegacy:        "legacy",
		TierCustomDirOnly: "custom-dir-only",
		TierAutoConnect:   "auto-connect",
		TierUnknown:       "unknown",
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", tier, got, want)
		}
	}
}
