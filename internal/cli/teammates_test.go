package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mvanhorn/agentcookie/internal/chrome"
	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/keystore"
	"github.com/mvanhorn/agentcookie/internal/state"
)

// newTestHub builds a hub over configDir with a temp state file and
// installs it as the daemon's active hub for the test.
func newTestHub(t *testing.T, configDir string, legacyPeers ...string) (*teammateHub, *bytes.Buffer) {
	t.Helper()
	store, err := state.OpenTeammateStore(filepath.Join(t.TempDir(), "teammates-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	hub := newTeammateHub(configDir, store, legacyPeers)
	var logBuf bytes.Buffer
	hub.log = &logBuf
	prev := activeTeammates
	activeTeammates = hub
	t.Cleanup(func() { activeTeammates = prev })
	return hub, &logBuf
}

func writeTeammatesFile(t *testing.T, configDir, body string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(configDir, config.TeammatesFile), body)
}

func TestTeammateHubNoFileIsInactive(t *testing.T) {
	dir := t.TempDir()
	hub, _ := newTestHub(t, dir)
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if hub.Active() || hub.IsListed("grokbot") || hub.IsTeammateName("grokbot") || hub.SuppressPush("grokbot") {
		t.Fatal("no teammates.yaml must leave teammate mode off")
	}
	if hub.Suspended() != nil {
		t.Fatal("no file is not a suspension")
	}
}

func TestTeammateHubReloadAddsAndRemoves(t *testing.T) {
	dir := t.TempDir()
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n  - name: instinct\n")
	hub, _ := newTestHub(t, dir)
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if !hub.Active() || !hub.IsListed("grokbot") || !hub.IsListed("instinct") {
		t.Fatal("listed teammates not active")
	}
	if rec, ok := hub.store.Record("grokbot"); !ok || rec.ListedAt.IsZero() {
		t.Fatalf("grokbot lifecycle entry missing: %+v", rec)
	}

	writeTeammatesFile(t, dir, "teammates:\n  - name: instinct\n")
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if hub.IsListed("grokbot") {
		t.Fatal("removed teammate still listed after one reload")
	}
	if !hub.IsTeammateName("grokbot") {
		t.Fatal("a removed teammate's name must stay claimed so it never falls back to non-teammate rules")
	}
	if rec, _ := hub.store.Record("grokbot"); rec.RemovedAt.IsZero() {
		t.Fatal("removal not recorded")
	}
}

// Covers R8 / KTD5: a syntax error suspends every teammate, including the
// one the broken edit meant to remove, and the error is kept for doctor.
func TestTeammateHubParseErrorSuspendsAll(t *testing.T) {
	dir := t.TempDir()
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n  - name: instinct\n")
	hub, _ := newTestHub(t, dir)
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	writeTeammatesFile(t, dir, "teammates:\n  - name: instinct\n  - name: [\n")
	if err := hub.Reload(); err == nil {
		t.Fatal("broken file should return its error")
	}
	if hub.Suspended() == nil {
		t.Fatal("parse failure must suspend teammate mode")
	}
	if hub.IsListed("grokbot") || hub.IsListed("instinct") {
		t.Fatal("no teammate may be listed while suspended")
	}
	if !hub.Active() {
		t.Fatal("a broken file still means teammates exist (fail closed)")
	}
	if snap := hub.store.Snapshot(); !strings.Contains(snap.ConfigError, config.TeammatesFile) {
		t.Fatalf("config error not recorded for doctor: %q", snap.ConfigError)
	}

	writeTeammatesFile(t, dir, "teammates:\n  - name: instinct\n")
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if hub.Suspended() != nil || !hub.IsListed("instinct") || hub.IsListed("grokbot") {
		t.Fatal("a good file should lift the suspension")
	}
	if snap := hub.store.Snapshot(); snap.ConfigError != "" {
		t.Fatalf("config error not cleared: %q", snap.ConfigError)
	}
}

func TestTeammateHubOnReloadFiresPerReload(t *testing.T) {
	dir := t.TempDir()
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	hub, _ := newTestHub(t, dir)
	var calls int
	hub.OnReload(func() { calls++ })
	_ = hub.Reload()
	_ = hub.Reload()
	if calls != 2 {
		t.Fatalf("OnReload calls = %d, want 2", calls)
	}
}

// Adding dot while running creates its lifecycle entry without a restart,
// and an fsnotify-driven edit is picked up within seconds.
func TestTeammateHubWatchPicksUpEdits(t *testing.T) {
	prev := teammatesReloadDebounce
	teammatesReloadDebounce = 20 * time.Millisecond
	t.Cleanup(func() { teammatesReloadDebounce = prev })

	dir := t.TempDir()
	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n")
	hub, _ := newTestHub(t, dir)
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- hub.Watch(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(50 * time.Millisecond) // let the watcher subscribe

	writeTeammatesFile(t, dir, "teammates:\n  - name: grokbot\n  - name: dot\n")
	waitFor(t, 5*time.Second, func() bool {
		_, ok := hub.store.Record("dot")
		return ok && hub.IsListed("dot")
	}, "dot was not picked up from the file edit")

	writeTeammatesFile(t, dir, "teammates:\n  - name: dot\n")
	waitFor(t, 5*time.Second, func() bool { return !hub.IsListed("grokbot") }, "grokbot removal was not picked up")
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// Covers R12: legacy `sink: instinct` plus instinct listed makes zero POSTs
// to instinct over three ticks, keeps keys/instinct.json, never rewrites
// source.yaml, and logs the hint once.
func TestLegacySinkMigratedWhenListed(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	instinctURL := "http://instinct:9999/sync"
	otherURL := "http://other.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: instinctURL, Peer: "instinct"}, {URL: otherURL}}
	// instinct's key equals the capture secret so a POST to it would be
	// captured (and fail this test) rather than error out unseen.
	if err := keystore.Save(fx.configDir, &keystore.PeerKey{Peer: "instinct", Key: []byte(fx.secret)}); err != nil {
		t.Fatal(err)
	}
	sourceYAML := "sinks:\n  - url: " + instinctURL + "\n    peer: instinct\n"
	writeCLIFile(t, filepath.Join(fx.configDir, "source.yaml"), sourceYAML)

	// Before listing: instinct is a normal push sink.
	if _, err := fx.push(); err != nil {
		t.Fatalf("baseline push: %v", err)
	}
	if got := countURL(fx.capture.postedURLs(), instinctURL); got != 1 {
		t.Fatalf("baseline should push instinct once, got %d", got)
	}

	writeTeammatesFile(t, fx.configDir, "teammates:\n  - name: instinct\n")
	hub, logBuf := newTestHub(t, fx.configDir, "instinct")
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(fx.capture.postedURLs())
	for range 3 {
		fx.forgetDeliveries()
		if _, err := fx.push(); err != nil {
			t.Fatalf("push: %v", err)
		}
		_ = hub.Reload()
	}
	posted := fx.capture.postedURLs()[before:]
	if got := countURL(posted, instinctURL); got != 0 {
		t.Fatalf("migrated instinct got %d POSTs over three ticks, want 0", got)
	}
	if got := countURL(posted, otherURL); got != 3 {
		t.Fatalf("other sink should still get 3 POSTs, got %d", got)
	}
	if _, err := keystore.Load(fx.configDir, "instinct"); err != nil {
		t.Fatalf("instinct key must be kept: %v", err)
	}
	if !hub.store.IsMigrated("instinct") {
		t.Fatal("instinct not recorded in migrated_legacy")
	}
	if n := strings.Count(logBuf.String(), "source.yaml"); n != 1 {
		t.Fatalf("migration hint logged %d times, want once: %q", n, logBuf.String())
	}
	if got := readCLIFile(t, filepath.Join(fx.configDir, "source.yaml")); got != sourceYAML {
		t.Fatalf("source.yaml was rewritten:\n%s", got)
	}
	if _, err := config.LoadSource(fx.configDir); err != nil {
		t.Fatalf("unchanged source.yaml should still load: %v", err)
	}

	// Covers R1: removing instinct after migration keeps push suppressed.
	writeTeammatesFile(t, fx.configDir, "teammates:\n  - name: grokbot\n")
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	before = len(fx.capture.postedURLs())
	for range 3 {
		if _, err := fx.push(); err != nil {
			t.Fatalf("push after removal: %v", err)
		}
	}
	if got := countURL(fx.capture.postedURLs()[before:], instinctURL); got != 0 {
		t.Fatalf("removed migrated teammate got %d POSTs, want 0 (removal must never re-enable push)", got)
	}
}

func TestNoTeammatesFileLeavesPushUnchanged(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	instinctURL := "http://instinct:9999/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: instinctURL, Peer: "instinct"}}
	if err := keystore.Save(fx.configDir, &keystore.PeerKey{Peer: "instinct", Key: []byte(fx.secret)}); err != nil {
		t.Fatal(err)
	}
	hub, _ := newTestHub(t, fx.configDir, "instinct")
	if err := hub.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.push(); err != nil {
		t.Fatal(err)
	}
	if got := countURL(fx.capture.postedURLs(), instinctURL); got != 1 {
		t.Fatalf("without teammates.yaml instinct must still be pushed, got %d", got)
	}
}

func countURL(urls []string, want string) int {
	n := 0
	for _, u := range urls {
		if u == want {
			n++
		}
	}
	return n
}

func readCLIFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A removed migrated legacy peer stays off push when the state file that
// remembers its migration cannot be read: every named push is held (and
// reported as failed) until teammate history can be checked again. A
// peerless legacy sink is not a teammate name and is still pushed.
func TestPushHeldWhileTeammateStateUnavailable(t *testing.T) {
	fx := newSourcePushFixture(t, []chrome.Cookie{
		{HostKey: ".example.com", Name: "s", Value: "v", Path: "/"},
	})
	instinctURL := "http://instinct:9999/sync"
	otherURL := "http://other.test/sync"
	fx.cfg.Sink = config.SinkRef{}
	fx.cfg.Sinks = []config.SinkTarget{{URL: instinctURL, Peer: "instinct"}, {URL: otherURL}}
	if err := keystore.Save(fx.configDir, &keystore.PeerKey{Peer: "instinct", Key: []byte(fx.secret)}); err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"", "teammates:\n  - name: grokbot\n"} {
		if list != "" {
			writeTeammatesFile(t, fx.configDir, list)
		}
		hub := newTeammateHub(fx.configDir, nil, []string{"instinct"})
		hub.log = io.Discard
		_ = hub.Reload()
		prev := activeTeammates
		activeTeammates = hub

		if !hub.SuppressPush("instinct") {
			t.Fatalf("list %q: SuppressPush(instinct) = false with the state store unavailable", list)
		}
		before := len(fx.capture.postedURLs())
		fx.forgetDeliveries()
		_, err := fx.push()
		activeTeammates = prev
		if err == nil || !strings.Contains(err.Error(), "teammate state") {
			t.Fatalf("list %q: held push to instinct not reported as a failure: %v", list, err)
		}
		posted := fx.capture.postedURLs()[before:]
		if got := countURL(posted, instinctURL); got != 0 {
			t.Fatalf("list %q: instinct got %d POSTs while teammate history is unknown, want 0", list, got)
		}
		if got := countURL(posted, otherURL); got != 1 {
			t.Fatalf("list %q: peerless sink got %d POSTs, want 1", list, got)
		}
	}
}
