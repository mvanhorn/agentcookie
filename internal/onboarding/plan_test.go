package onboarding

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSourceRequiresBrowserAndHoldingDecision(t *testing.T) {
	if _, err := SourcePlan(SourceInput{}); err == nil {
		t.Fatal("missing browser accepted")
	}
	in := SourceInput{Browser: "zen", Profile: "/tmp/profile", Reader: "auto", StorageReadMode: "request", HeldSites: []string{"example.com: Personal, Work"}}
	if _, err := SourcePlan(in); err == nil {
		t.Fatal("unresolved sites silently accepted")
	}
	in.AcceptHeldSites = true
	got, err := SourcePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"name: zen", "profile: /tmp/profile", "storage_read_mode: request", "companion_port: 9226", "containers: []"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in %s", s, got)
		}
	}
}
func TestSourceRejectsInvalidPortsAndModes(t *testing.T) {
	for _, in := range []SourceInput{{Browser: "zen", BiDiPort: 80}, {Browser: "zen", BiDiPort: 9226, CompanionPort: 9226}, {Browser: "zen", Reader: "magic"}, {Browser: "zen", StorageReadMode: "magic"}} {
		if _, err := SourcePlan(in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
}
func TestSourceYAMLQuotesUserInputs(t *testing.T) {
	got, err := SourcePlan(SourceInput{Browser: "zen", Profile: "a\npeer: evil", Containers: []string{"Work: one"}, ContainerSites: map[string][]string{"Work: one": {"example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\npeer: evil") {
		t.Fatal("YAML injection")
	}
}
func TestDestinationDefaults(t *testing.T) {
	for _, tc := range []struct {
		os   string
		gui  bool
		want string
	}{{"darwin", true, "live_chrome"}, {"darwin", false, "attach"}, {"linux", true, "attach"}} {
		if got := DefaultDestination(tc.os, tc.gui); got != tc.want {
			t.Fatalf("got %s", got)
		}
	}
}
func TestReadyBeforeWindowAndDaemon(t *testing.T) {
	var steps []string
	hooks := ReadyHooks{Prepare: func(context.Context) error { steps = append(steps, "prepare"); return nil }, Restore: func(context.Context) (bool, error) { steps = append(steps, "restore"); return true, nil }, Open: func(context.Context) error { steps = append(steps, "open"); return nil }, Install: func(context.Context) error { steps = append(steps, "daemon"); return nil }}
	if err := ReadyBeforeInstall(context.Background(), hooks); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"prepare", "restore", "open", "daemon"}) {
		t.Fatal(steps)
	}
	for _, failure := range []error{nil, errors.New("repair-needed")} {
		steps = nil
		hooks.Restore = func(context.Context) (bool, error) { return false, failure }
		if err := ReadyBeforeInstall(context.Background(), hooks); err == nil {
			t.Fatal("unready accepted")
		}
		if !reflect.DeepEqual(steps, []string{"prepare"}) {
			t.Fatal(steps)
		}
	}
}
func TestChromeLauncherQuotesPaths(t *testing.T) {
	got, err := ChromeLauncher("/tmp/a ' quote/agentcookie", "/tmp/config ' x", "work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "'\"'\"'") || !strings.Contains(got, "destination open 'work'") {
		t.Fatal(got)
	}
	if strings.Contains(got, "--remote-debugging") {
		t.Fatal("launcher bypasses sink readiness")
	}
	if _, err := ChromeLauncher("relative", "/tmp/config", "work"); err == nil {
		t.Fatal("relative executable accepted")
	}
}
