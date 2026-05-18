package cli

import (
	"strings"
	"testing"
)

func TestLastJSONLine_FindsLastJSONObject(t *testing.T) {
	stdout := `some log line
agentcookie: strategy partition-list:apple-tool,apple
{"name":"partition-list:apple-tool,apple","success":false}
{"winning_strategy":"trust-list:instacart-pp-cli","overall_success":true,"attempts":[]}`
	got := lastJSONLine(stdout)
	if !strings.HasPrefix(got, `{"winning_strategy"`) {
		t.Errorf("expected last JSON line, got %q", got)
	}
}

func TestLastJSONLine_NoJSON(t *testing.T) {
	stdout := "just some log\nlines here"
	if got := lastJSONLine(stdout); got != "" {
		t.Errorf("no JSON expected, got %q", got)
	}
}

func TestLastJSONLine_TrailingNewlinesIgnored(t *testing.T) {
	stdout := `{"a":1}
{"b":2}


`
	got := lastJSONLine(stdout)
	if got != `{"b":2}` {
		t.Errorf("got %q, want %q", got, `{"b":2}`)
	}
}

func TestBuildStrategies_PrimaryIsDeleteAndRecreate(t *testing.T) {
	s := buildStrategies(nil)
	if len(s) < 1 {
		t.Fatal("expected at least one default strategy")
	}
	// v0.12: primary strategy moved from -A (any app) to -T (per-binary
	// trust list anchored on the Developer-ID-signed designated
	// requirement). v0.10 -A is no longer in the strategy chain.
	if s[0].name != "delete-and-recreate-with-T" {
		t.Errorf("primary strategy: got %q, want delete-and-recreate-with-T (v0.12 ACL pin via Developer ID signed binary)", s[0].name)
	}
}

func TestBuildStrategies_PartitionListFallbackComesSecond(t *testing.T) {
	s := buildStrategies(nil)
	if len(s) < 2 {
		t.Fatal("expected partition-list fallback to exist")
	}
	if !strings.HasPrefix(s[1].name, "partition-list:") {
		t.Errorf("second strategy should be partition-list fallback, got %q", s[1].name)
	}
}

func TestBuildStrategies_ExtraBinariesAppearAfterDefaultStrategies(t *testing.T) {
	defaults := buildStrategies(nil)
	withExtras := buildStrategies([]string{"/Users/me/go/bin/instacart-pp-cli", "/Users/me/go/bin/bird"})
	if len(withExtras) != len(defaults)+2 {
		t.Fatalf("expected %d default + 2 extra-binary strategies, got %d", len(defaults), len(withExtras))
	}
	last2 := withExtras[len(defaults):]
	if !strings.Contains(last2[0].name, "trust-list:") || !strings.Contains(last2[0].name, "instacart-pp-cli") {
		t.Errorf("first extra strategy: got %q, want trust-list:instacart-pp-cli", last2[0].name)
	}
	if !strings.Contains(last2[1].name, "trust-list:") || !strings.Contains(last2[1].name, "bird") {
		t.Errorf("second extra strategy: got %q, want trust-list:bird", last2[1].name)
	}
}

func TestRandomKeychainPassword_NonEmptyAndUnique(t *testing.T) {
	a := randomKeychainPassword()
	b := randomKeychainPassword()
	if a == "" {
		t.Error("password is empty")
	}
	if a == b {
		t.Errorf("two consecutive calls returned same value: %q", a)
	}
	if len(a) < 16 {
		t.Errorf("password unexpectedly short (%d chars): %q", len(a), a)
	}
}
