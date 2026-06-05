package agentattach

import (
	"strings"
	"testing"
)

func TestClassifyLoggedOut(t *testing.T) {
	cases := []struct {
		name      string
		in        PreflightInput
		wantCause LoggedOutCause
		wantInRem string
	}{
		{
			name:      "blocklist wins over everything",
			in:        PreflightInput{DomainBlocklisted: true, Reachable: false, Wired: false},
			wantCause: CauseBlocklisted,
			wantInRem: "blocklist",
		},
		{
			name:      "endpoint down on 144+ points to chrome://inspect",
			in:        PreflightInput{Reachable: false, Tier: TierAutoConnect},
			wantCause: CauseDebuggingOff,
			wantInRem: "chrome://inspect",
		},
		{
			name:      "endpoint down on old chrome points to fallback",
			in:        PreflightInput{Reachable: false, Tier: TierCustomDirOnly},
			wantCause: CauseDebuggingOff,
			wantInRem: "--fallback",
		},
		{
			name:      "reachable but not wired",
			in:        PreflightInput{Reachable: true, Wired: false},
			wantCause: CauseNotWired,
			wantInRem: "--wire",
		},
		{
			name:      "dbsc on debug profile",
			in:        PreflightInput{Reachable: true, Wired: true, OnDebugProfile: true, DomainDBSCBound: true},
			wantCause: CauseDBSCBound,
			wantInRem: "device-bound",
		},
		{
			name:      "dbsc on real profile is fine (attach carries it)",
			in:        PreflightInput{Reachable: true, Wired: true, OnDebugProfile: false, DomainDBSCBound: true},
			wantCause: CauseNone,
		},
		{
			name:      "all good",
			in:        PreflightInput{Reachable: true, Wired: true},
			wantCause: CauseNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cause, rem := ClassifyLoggedOut(c.in)
			if cause != c.wantCause {
				t.Errorf("cause = %v, want %v", cause, c.wantCause)
			}
			if c.wantInRem != "" && !strings.Contains(rem, c.wantInRem) {
				t.Errorf("remediation %q missing %q", rem, c.wantInRem)
			}
			if c.wantCause == CauseNone && rem != "" {
				t.Errorf("CauseNone should have empty remediation, got %q", rem)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	if got := Summary(CauseNone, ""); !strings.Contains(got, "looks attached") {
		t.Errorf("CauseNone summary = %q", got)
	}
	if got := Summary(CauseNotWired, "run --wire"); !strings.Contains(got, "not-wired") || !strings.Contains(got, "run --wire") {
		t.Errorf("summary = %q", got)
	}
}
