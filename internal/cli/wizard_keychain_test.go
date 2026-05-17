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

func TestBuildStrategies_DefaultsToTwoPartitionStrategies(t *testing.T) {
	s := buildStrategies(nil)
	if len(s) != 2 {
		t.Errorf("default strategy count: got %d, want 2", len(s))
	}
	if !strings.Contains(s[0].name, "apple-tool,apple") || strings.Contains(s[0].name, "teamid") {
		t.Errorf("first strategy should be apple-tool,apple without teamid, got %q", s[0].name)
	}
	if !strings.Contains(s[1].name, "teamid") {
		t.Errorf("second strategy should include teamid, got %q", s[1].name)
	}
}

func TestBuildStrategies_ExtraBinariesAppearAsTrustListStrategies(t *testing.T) {
	s := buildStrategies([]string{"/Users/me/go/bin/instacart-pp-cli", "/Users/me/go/bin/bird"})
	if len(s) != 4 {
		t.Fatalf("expected 2 default + 2 extra-binary strategies, got %d", len(s))
	}
	if !strings.Contains(s[2].name, "trust-list:") || !strings.Contains(s[2].name, "instacart-pp-cli") {
		t.Errorf("third strategy: got %q, want trust-list:instacart-pp-cli", s[2].name)
	}
	if !strings.Contains(s[3].name, "trust-list:") || !strings.Contains(s[3].name, "bird") {
		t.Errorf("fourth strategy: got %q, want trust-list:bird", s[3].name)
	}
}

func TestBuildStrategies_PartitionListStrategiesComeFirst(t *testing.T) {
	// Sequence matters: partition list is the cheaper, more universal fix.
	// Per-binary trust list is a fallback. Verify ordering.
	s := buildStrategies([]string{"/path/to/cli"})
	for i := 0; i < 2; i++ {
		if !strings.HasPrefix(s[i].name, "partition-list:") {
			t.Errorf("strategy %d should be partition-list, got %q", i, s[i].name)
		}
	}
	if !strings.HasPrefix(s[2].name, "trust-list:") {
		t.Errorf("strategy 2 should be trust-list, got %q", s[2].name)
	}
}
