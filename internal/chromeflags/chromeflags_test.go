package chromeflags

import (
	"strings"
	"testing"
)

func TestDisabledFeaturesMergesAndDedupes(t *testing.T) {
	cases := map[string]string{
		"":                      "MacAppCodeSignClone",
		"Translate":             "Translate,MacAppCodeSignClone",
		"MacAppCodeSignClone,A": "MacAppCodeSignClone,A",
		" A , ,B":               "A,B,MacAppCodeSignClone",
	}
	for in, want := range cases {
		if got := DisabledFeatures(in); got != want {
			t.Errorf("DisabledFeatures(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChromedpDefaultsCarried guards against chromedp changing its default
// --disable-features value: our override replaces it, so it must be a superset.
func TestChromedpDefaultsCarried(t *testing.T) {
	got := ChromedpDisabledFeatures()
	for _, f := range strings.Split(chromedpDefaultDisabled, ",") {
		if !strings.Contains(","+got+",", ","+f+",") {
			t.Errorf("ChromedpDisabledFeatures() = %q, missing chromedp default %q", got, f)
		}
	}
}
