package state

import "testing"

func TestSinkForSeparatesDestinationHistory(t *testing.T) {
	s := &SourceState{}
	s.SinkFor("mini", "http://mini/sync", "personal").TotalFailures = 3
	s.SinkFor("mini", "http://mini/sync", "work").TotalPushes = 7
	s.SinkFor("mini", "http://mini/sync").TotalPushes = 2
	if len(s.Sinks) != 3 {
		t.Fatal("destination states collide")
	}
	if got := s.SinkFor("mini", "http://new-mini/sync", "personal"); got.TotalFailures != 3 || got.TotalPushes != 0 || got.URL != "http://new-mini/sync" {
		t.Fatalf("bad state %+v", got)
	}
	if got := s.SinkFor("mini", "http://mini/sync", "work"); got.TotalFailures != 0 || got.TotalPushes != 7 {
		t.Fatalf("bad work state %+v", got)
	}
}
