package cli

import (
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/destination"
	"github.com/mvanhorn/agentcookie/internal/sinkpush"
	"github.com/mvanhorn/agentcookie/internal/state"
	"strings"
	"testing"
	"time"
)

func TestDestinationStallRequiresContinuingDelivery(t *testing.T) {
	now := time.Unix(1800000000, 0)
	base := destination.Status{Name: "owned", Running: true, PendingSince: now.Add(-11 * time.Minute), LastAcceptedAt: now.Add(-time.Minute)}
	for _, tc := range []struct {
		name            string
		required, ready bool
		since, accepted time.Time
		want            bool
	}{
		{"stalled", true, false, base.PendingSince, base.LastAcceptedAt, true},
		{"optional", false, false, base.PendingSince, base.LastAcceptedAt, false},
		{"ready", true, true, base.PendingSince, base.LastAcceptedAt, false},
		{"new", true, false, now.Add(-time.Minute), base.LastAcceptedAt, false},
		{"stale source", true, false, base.PendingSince, now.Add(-11 * time.Minute), false},
		{"old state", true, false, time.Time{}, time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			s.Ready = tc.ready
			s.PendingSince = tc.since
			s.LastAcceptedAt = tc.accepted
			if got := destinationStalled(tc.required, s, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestDoctorDestinationProbesExplicitReadyAndBooleanOnly(t *testing.T) {
	cfg := &config.SinkConfig{Destinations: []config.DestinationRef{{Name: "owned", Kind: "live_chrome", Required: true, Chrome: config.DestinationChrome{UserDataDir: t.TempDir(), Port: 19324}}}}
	st := &state.SinkState{Destinations: []destination.Status{{Name: "owned", Ready: true, Running: true, Accepted: 2, Applied: map[string]uint64{"default": 2}}}}
	calls := 0
	d := doctorDeps{DestinationProbe: func(config.DestinationRef, []sinkpush.VerifySpec) []sinkpush.VerifyResult {
		calls++
		return []sinkpush.VerifyResult{{Host: "github.com", State: sinkpush.AuthYes, Detail: "secret must never appear"}}
	}}
	out := checkDoctorDestinations(cfg, st, d)
	if calls != 0 || len(out) != 1 || out[0].Severity != SeverityOK {
		t.Fatalf("implicit probe or invalid status: %+v", out)
	}
	cfg.Destinations[0].ProbeHosts = []string{"github.com"}
	out = checkDoctorDestinations(cfg, st, d)
	if calls != 1 || len(out) != 2 || out[1].Detail != "authenticated" {
		t.Fatalf("probe absent: %+v", out)
	}
	for _, c := range out {
		if strings.Contains(c.Detail, "secret") {
			t.Fatal("probe value leaked")
		}
	}
	st.Destinations[0].Ready = false
	_ = checkDoctorDestinations(cfg, st, d)
	if calls != 1 {
		t.Fatal("probed unready destination")
	}
}

func TestDoctorDestinationGenerationsAreNotAdded(t *testing.T) {
	s := destination.Status{Running: true, Ready: true, Accepted: 10, Applied: map[string]uint64{"default/cookies": 10, "default/localStorage": 10, "default/indexedDB": 10}}
	c, _ := doctorDestinationStatus(config.DestinationRef{Name: "owned"}, []destination.Status{s}, time.Now())
	if strings.Contains(c.Detail, "applied=30") || !strings.Contains(c.Detail, `accepted=10 applied={"default/cookies":10,"default/indexedDB":10,"default/localStorage":10}`) {
		t.Fatal(c.Detail)
	}
}
