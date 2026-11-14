package cli

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

// sweepCall is one call the fake liveCDPSweep saw.
type sweepCall struct {
	endpoint string
	minAge   time.Duration
	dryRun   bool
}

// fakeLiveCDPSweep replaces liveCDPSweep for the test. It records each call
// and returns ids, as the stale tabs a sweep closed or would close.
func fakeLiveCDPSweep(t *testing.T, ids ...target.ID) chan sweepCall {
	t.Helper()
	calls := make(chan sweepCall, 4)
	prev := liveCDPSweep
	liveCDPSweep = func(_ context.Context, endpoint string, minAge time.Duration, dryRun bool) ([]target.ID, error) {
		calls <- sweepCall{endpoint, minAge, dryRun}
		return ids, nil
	}
	t.Cleanup(func() { liveCDPSweep = prev })
	return calls
}

// TestRunSinkStartsNoLiveCDPSweep drives the real runSink startup path. On
// Linux live_cdp points at the agents' own Chrome, where an idle
// Playwright or Puppeteer page looks like a leaked blank tab, so a sink
// start must never sweep. The listen port is taken up front, so runSink
// wires its background work and then fails to bind.
func TestRunSinkStartsNoLiveCDPSweep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	withConfigDir(t, dir)

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })

	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen:\n  addr: "+busy.Addr().String()+
		"\npeer:\n  hostname: 127.0.0.1\nsecurity:\n  shared_secret: startup-wiring-test-secret-32-bytes-long\n"+
		"skip_chrome_sqlite: true\nlive_cdp:\n  enabled: true\n  endpoint: http://127.0.0.1:1\n")
	calls := fakeLiveCDPSweep(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if err := runSink(cmd, nil); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("runSink = %v, want it to reach the bind and fail there", err)
	}
	select {
	case got := <-calls:
		t.Fatalf("runSink started a stale-tab sweep at %s", got.endpoint)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestTeammatePullLoopStartsNoLiveCDPSweep: the pull loop applies through
// the same live CDP path and must not sweep either.
func TestTeammatePullLoopStartsNoLiveCDPSweep(t *testing.T) {
	ts := newTeammateSink(t, "turn+timer", "")
	path := filepath.Join(ts.dir, "sink.yaml")
	yml, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, path, strings.Replace(string(yml), "live_cdp:\n", "live_cdp:\n  enabled: true\n", 1))
	calls := fakeLiveCDPSweep(t)
	prevInterval := teammatePullInterval
	teammatePullInterval = func() time.Duration { return time.Hour }
	t.Cleanup(func() { teammatePullInterval = prevInterval })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runTeammatePullLoop(ctx) }()
	select {
	case got := <-calls:
		t.Errorf("the pull loop started a stale-tab sweep at %s", got.endpoint)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	<-done
}

// runSweepTabsCommand runs `agentcookie sink sweep-tabs args...` through
// cobra, so the flags are wired as a user would type them.
func runSweepTabsCommand(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	oldDir := common.ConfigDir
	t.Cleanup(func() { common.ConfigDir = oldDir })
	sinkSweepTabsOpts = sinkSweepTabsOptions{}
	t.Cleanup(func() { sinkSweepTabsOpts = sinkSweepTabsOptions{} })

	root := &cobra.Command{Use: "agentcookie", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&common.ConfigDir, "config-dir", defaultConfigDir(), "config dir")
	root.AddCommand(sinkCmd)
	t.Cleanup(func() { root.RemoveCommand(sinkCmd) })
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetArgs(append([]string{"--config-dir", dir, "sink", "sweep-tabs"}, args...))
	err := root.Execute()
	return out.String(), err
}

func writeLiveCDPSinkYAML(t *testing.T, dir, liveCDP string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(dir, "sink.yaml"), "listen:\n  addr: 127.0.0.1:9999\n"+
		"peer:\n  hostname: 127.0.0.1\nsecurity:\n  shared_secret: sweep-tabs-test-secret-32-bytes-long!\n"+
		"skip_chrome_sqlite: true\nlive_cdp:\n"+liveCDP)
}

func TestSinkSweepTabs_DryRunListsAndClosesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeLiveCDPSinkYAML(t, dir, "  enabled: true\n  endpoint: http://127.0.0.1:9333\n")
	calls := fakeLiveCDPSweep(t, "TAB1", "TAB2")

	out, err := runSweepTabsCommand(t, dir, "--dry-run")
	if err != nil {
		t.Fatalf("sweep-tabs --dry-run: %v", err)
	}
	got := <-calls
	if got != (sweepCall{"http://127.0.0.1:9333", livecdp.DefaultSweepMinAge, true}) {
		t.Fatalf("sweep call = %+v, want the configured endpoint, the default age, and dry run", got)
	}
	for _, want := range []string{"Would close 2 stale about:blank tab(s) at http://127.0.0.1:9333", "TAB1", "TAB2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Closed") {
		t.Errorf("dry run reports closing tabs:\n%s", out)
	}
}

func TestSinkSweepTabs_ClosesAtEndpointOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	calls := fakeLiveCDPSweep(t, "TAB1", "TAB2", "TAB3")

	// --endpoint needs no sink.yaml.
	out, err := runSweepTabsCommand(t, dir, "--endpoint", "http://127.0.0.1:9444")
	if err != nil {
		t.Fatalf("sweep-tabs: %v", err)
	}
	if got := <-calls; got != (sweepCall{"http://127.0.0.1:9444", livecdp.DefaultSweepMinAge, false}) {
		t.Fatalf("sweep call = %+v, want the --endpoint override and a real run", got)
	}
	if want := "Closed 3 stale about:blank tab(s) at http://127.0.0.1:9444"; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestSinkSweepTabs_UsesDefaultEndpointWhenUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeLiveCDPSinkYAML(t, dir, "  enabled: true\n")
	calls := fakeLiveCDPSweep(t)

	out, err := runSweepTabsCommand(t, dir)
	if err != nil {
		t.Fatalf("sweep-tabs: %v", err)
	}
	if got := <-calls; got.endpoint != livecdp.DefaultCDPEndpoint {
		t.Fatalf("swept %s, want the default live_cdp endpoint %s", got.endpoint, livecdp.DefaultCDPEndpoint)
	}
	if !strings.Contains(out, "Closed 0 stale about:blank tab(s)") {
		t.Errorf("output does not report the empty sweep:\n%s", out)
	}
}

// Without live_cdp on, sink.yaml names no Chrome the sink owns, so the
// command will not guess one.
func TestSinkSweepTabs_RefusesWithoutLiveCDP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeLiveCDPSinkYAML(t, dir, "  enabled: false\n")
	calls := fakeLiveCDPSweep(t)

	_, err := runSweepTabsCommand(t, dir)
	if err == nil || !strings.Contains(err.Error(), "--endpoint") {
		t.Fatalf("sweep-tabs with live_cdp off = %v, want an error naming --endpoint", err)
	}
	select {
	case got := <-calls:
		t.Fatalf("swept %s with live_cdp off", got.endpoint)
	default:
	}
}
