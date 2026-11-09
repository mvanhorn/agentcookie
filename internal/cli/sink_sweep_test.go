package cli

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/mvanhorn/agentcookie/internal/config"
	"github.com/mvanhorn/agentcookie/internal/livecdp"
)

func TestLiveCDPSweepStartsOnlyWhenLiveCDPModifiesChrome(t *testing.T) {
	calls := make(chan string, 1)
	prev := liveCDPSweep
	liveCDPSweep = func(_ context.Context, endpoint string, minAge time.Duration) (int, error) {
		if minAge != livecdp.DefaultSweepMinAge {
			t.Errorf("sweep minAge = %s, want %s", minAge, livecdp.DefaultSweepMinAge)
		}
		calls <- endpoint
		return 0, nil
	}
	t.Cleanup(func() { liveCDPSweep = prev })

	cfg := &config.SinkConfig{}
	if startLiveCDPSweep(context.Background(), cfg) {
		t.Fatal("sweep started with live_cdp disabled")
	}
	cfg.LiveCDP = config.LiveCDPRef{Enabled: true, Endpoint: "http://127.0.0.1:9333"}
	prevDry := sinkDryRun
	t.Cleanup(func() { sinkDryRun = prevDry })
	sinkDryRun = true
	if startLiveCDPSweep(context.Background(), cfg) {
		t.Fatal("sweep started in --dry-run")
	}
	sinkDryRun = false
	if !startLiveCDPSweep(context.Background(), cfg) {
		t.Fatal("sweep not started with live_cdp on and --dry-run off")
	}
	select {
	case got := <-calls:
		if got != "http://127.0.0.1:9333" {
			t.Fatalf("sweep endpoint = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sweep never ran")
	}
}

func TestRunLiveCDPSweepPassesUntilClean(t *testing.T) {
	prev := liveCDPSweep
	t.Cleanup(func() { liveCDPSweep = prev })

	run := func(results []int, errs []error) int {
		calls := 0
		liveCDPSweep = func(ctx context.Context, _ string, _ time.Duration) (int, error) {
			if dl, ok := ctx.Deadline(); !ok || time.Until(dl) < liveCDPSweepPassBudget-time.Second {
				t.Errorf("pass %d: deadline %v, want a fresh %s budget", calls+1, dl, liveCDPSweepPassBudget)
			}
			i := calls
			calls++
			if i >= len(results) {
				return 0, nil
			}
			return results[i], errs[i]
		}
		runLiveCDPSweep(context.Background(), "http://127.0.0.1:9333")
		return calls
	}

	// A pass that runs out of time mid-pile is followed by another.
	timeout := context.DeadlineExceeded
	if got := run([]int{1200, 1100, 600, 0}, []error{timeout, timeout, nil, nil}); got != 4 {
		t.Fatalf("passes = %d, want 4 (until one closes nothing)", got)
	}
	// A pass that fails without closing anything ends the sweep.
	if got := run([]int{0}, []error{errors.New("unreachable")}); got != 1 {
		t.Fatalf("passes after a failed empty pass = %d, want 1", got)
	}
	// A browser that keeps growing blanks is capped.
	many := make([]int, 50)
	manyErrs := make([]error, 50)
	for i := range many {
		many[i] = 1
	}
	if got := run(many, manyErrs); got != liveCDPSweepMaxPasses {
		t.Fatalf("passes = %d, want the %d cap", got, liveCDPSweepMaxPasses)
	}
}

// TestRunSinkStartsLiveCDPSweep drives the real runSink startup path, not
// the sweep helper: the helper's own tests cannot see a missing call site.
// The listen port is taken up front, so runSink wires its background work
// and then fails to bind.
//
// Guard-revert: remove startLiveCDPSweep from runSink and this goes red.
func TestRunSinkStartsLiveCDPSweep(t *testing.T) {
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

	swept := make(chan string, 1)
	prev := liveCDPSweep
	liveCDPSweep = func(_ context.Context, endpoint string, _ time.Duration) (int, error) {
		select {
		case swept <- endpoint:
		default:
		}
		return 0, nil
	}
	t.Cleanup(func() { liveCDPSweep = prev })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if err := runSink(cmd, nil); err == nil || !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("runSink = %v, want it to reach the bind and fail there", err)
	}

	select {
	case got := <-swept:
		if got != "http://127.0.0.1:1" {
			t.Errorf("swept %q, want the configured live_cdp endpoint", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runSink never started the live CDP stale-tab sweep")
	}
}
